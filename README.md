# nginx-cache-purge
A tool to help purge Nginx cache. It can either run locally with the purge command, or run as a local unix service to allow for purging by Nginx http requests. The tool supports using wildcard/glob syntax in the purge key to match multiple keys from the cache.

## Install
You can install either by downloading the latest binary release, or by building.

## Building
Building should be as simple as running:

```
make
```

## Usage
The following are some examples of ways to purge cache 

### Purge a specific key
```
$ nginx-cache-purge purge /var/nginx/proxy_temp/cache example.com/index.html
```

### Purge all keys for a domain
```
$ nginx-cache-purge purge /var/nginx/proxy_temp/cache 'example.com/*'
```

### Purge all keys for jpeg and png files
```
$ nginx-cache-purge purge /var/nginx/proxy_temp/cache 'example.com/*.{jpg,jpeg,png}'
```

### Purge all keys
```
$ nginx-cache-purge purge /var/nginx/proxy_temp/cache '*'
```

### Purge a key that contains wildcard characters
Whether a key is a wildcard is otherwise guessed from the punctuation in it, and
real cache keys carry that punctuation: a request URI with a query string puts
`?` in the key, and PHP-style array parameters put `[` and `]`. `--exact` says
the key is a literal, for both the key and any excludes.

```
$ nginx-cache-purge purge --exact /var/nginx/proxy_temp/cache 'example.com/list.php?f[]=x'
```

## Running as a service
If you want to run as a service to allow purge requests via http requests, the
service can install itself:

```
nginx-cache-purge service install --cache-path /var/nginx/proxy_temp/cache
nginx-cache-purge service start
```

`service` also accepts `stop`, `restart`, `status`, and `uninstall`. The
installed unit runs `nginx-cache-purge server` as a notify service, creates the
runtime directory the socket lives in, and restarts on failure. `--cache-path`
restricts which caches that server will purge, and is covered below.

Connecting to a UNIX socket needs write permission on it, so the socket is
created mode `0660`: the purge server and Nginx have to run as the same user, or
share a group. The installed unit runs as root, so either add a
`User=`/`Group=` drop-in for it, or widen the socket with
`--socket-mode`.

### Restricting which caches may be purged
The directory to purge comes from the request, and the purge deletes what it
finds under it, so a server left unrestricted will purge any path its user can
reach. Pass `--cache-path` to name the caches it may serve, repeating it for
more than one:

```
nginx-cache-purge server --cache-path /var/nginx/proxy_temp/cache
```

A request naming any other directory is refused with `403`. Directories inside a
named cache are allowed, and symlinks are resolved, so a link to a named cache is
recognised as that cache. Naming no path leaves every path purgeable, which is
what a server without the flag has always done.

`service install` takes the same flag and writes it into the unit's `ExecStart`,
so an installed service is restricted from its first start:

```
nginx-cache-purge service install --cache-path /var/nginx/proxy_temp/cache
```

Repeat the flag for more than one cache. Paths are made absolute at install, as
the unit runs from a working directory of the service manager's choosing;
symlinks are left as written and resolved per request. The allowlist lives in the
unit, so changing it means installing again: `service uninstall` then
`service install` with the paths you want.

## Nginx config
If you want to purge via Nginx http requests, you'll need to add configuration to your Nginx config file.

The server reads four query parameters:

| Parameter | Description |
| --- | --- |
| `path` | Path to the cache directory, the same one given to `proxy_cache_path`. |
| `key` | Cache key or wildcard match, the same one built by `proxy_cache_key`. |
| `exclude` | Key to keep, can be a wildcard. Repeat it to exclude more than one. |
| `exact` | Read the key and excludes as literals rather than wildcards. |

Nginx substitutes `$request_uri` into the rewrite without escaping it, so a key
containing `%` or `+` arrives as the literal bytes Nginx stored it under, and
that is what is purged. A key escaped by a caller writing the request itself is
purged too, so either convention works.

### Literal keys and wildcards
A key built from `$request_uri` is a literal, and a request URI routinely holds
the punctuation that would otherwise mark the key as a wildcard: `?` from a query
string, `[` and `]` from PHP-style array parameters. Read as wildcards those keys
purge the wrong entries, fail outright, or match nothing while still answering
`PURGED` and leaving the stale entry served. The examples below therefore pass
`exact=1`, which is what you want when a PURGE request names one URL.

Leave `exact` off where the purge is meant to take a wildcard, such as the
`/purge(/.*)` location further down, where a request for `/purge/images/*`
clears everything under `/images/`.

Because `$request_uri` carries the client's own query string into the purge
parameters, put `exact=1` **before** `key=` in the rewrite. Parameters are taken
first-wins, so an `exact=0` a client appends to its request URI arrives second
and is ignored. The same ordering already protects `path`. Note that `exclude` is
collected rather than taken first-wins, so a client can append an `exclude` that
holds back its own purge.

### Map PURGE requests
```
http {
    map $request_method $is_purge {                                                             
        default   0;
        PURGE     1;
    }

    proxy_cache_path /var/nginx/proxy_temp/cache levels=1:2 keys_zone=my_cache:10m;
    proxy_cache_key $server_name$request_uri;

    server {
        location / {
            proxy_cache_bypass $is_purge;
            if ($is_purge) {
                proxy_pass http://unix:/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&exact=1&key=$server_name$request_uri break;
            }

            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
    }
}
```

### Auth via cookie
```
http {
    map $cookie_purge_token $is_purge {
        default 0;
        nnCgKUx1p2bIABXR 1;
    }

    proxy_cache_path /var/nginx/proxy_temp/cache levels=1:2 keys_zone=my_cache:10m;
    proxy_cache_key $server_name$request_uri;

    server {
        location / {
            proxy_cache_bypass $is_purge;
            if ($is_purge) {
                proxy_pass http://unix:/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&exact=1&key=$server_name$request_uri break;
            }

            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
    }
}
```

### Auth via header
```
http {
    map $http_purge_token $is_purge {
        default 0;
        nnCgKUx1p2bIABXR 1;
    }

    proxy_cache_path /var/nginx/proxy_temp/cache levels=1:2 keys_zone=my_cache:10m;
    proxy_cache_key $server_name$request_uri;

    server {
        location / {
            proxy_cache_bypass $is_purge;
            if ($is_purge) {
                proxy_pass http://unix:/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&exact=1&key=$server_name$request_uri break;
            }

            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
    }
}
```

### Auth via header and IP white list.
```
http {
    map $http_purge_token $is_purge {
        default 0;
        nnCgKUx1p2bIABXR 1;
    }

    geo $purge_allowed {
        default 0;
        127.0.0.1 1;
        192.168.0.0/24 1;
    }

    proxy_cache_path /var/nginx/proxy_temp/cache levels=1:2 keys_zone=my_cache:10m;
    proxy_cache_key $server_name$request_uri;

    server {
        location / {
            set $should_purge $purge_allowed;
            if ($is_purge != 1) {
                set $should_purge 0;
            }
            proxy_cache_bypass $should_purge;
            if ($should_purge) {
                proxy_pass http://unix:/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&exact=1&key=$server_name$request_uri break;
            }

            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
    }
}
```

### Using IP whitelists
This location takes a wildcard, so it leaves `exact` off: a request for
`/purge/images/*` clears everything under `/images/`.

```
http {
    proxy_cache_path /var/nginx/proxy_temp/cache levels=1:2 keys_zone=my_cache:10m;
    proxy_cache_key $server_name$request_uri;

    server {
        location / {
            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
        location ~ /purge(/.*) {
            allow 127.0.0.1;
            deny all;
            proxy_pass http://unix:/run/nginx-cache-purge/http.sock;
            rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$server_name$1 break;
        }
    }
}
```
