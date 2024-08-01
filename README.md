# nginx-cache-purge
A tool to help purge Nginx cache. It can either run locally with the purge command, or run as a local unix service to allow for purging by Nginx http requests. The tool supports using wildcard/glob syntax in the purge key to match multiple keys from the cache.

## Install
You can install either by downloading the latest binary release, or by building.

## Building
Building should be as simple as running:
```
go build
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

## Running as a service
If you want to run as a service to allow purge requests via http requests, you'll need to create a systemd service file and place it in `/etc/systemd/system/nginx-cache-purge.service`.
```
[Unit]
Description=Nginx Cache Purge
After=network.target
 
[Service]
User=nginx
Group=nginx
RuntimeDirectory=nginx-cache-purge
PIDFile=/var/run/nginx-cache-purge/service.pid
ExecStart=/usr/local/bin/nginx-cache-purge server
Restart=always
RestartSec=3s
 
[Install]
WantedBy=multi-user.target
```

You can then run the following to start the service:
```
systemctl daemon-reload
systemctl start nginx-cache-purge.service
```

## Nginx config
If you want to purge via Nginx http requests, you'll need to add configuration to your Nginx config file.

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
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$server_name$request_uri break;
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
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$server_name$request_uri break;
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
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$server_name$request_uri break;
            }

            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
    }
}
```

### Using IP whitelists
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
            proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
            rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$server_name$1 break;
        }
    }
}
```

## Help
```
$ nginx-cache-purge --help  
Usage: nginx-cache-purge <command> [flags]

Tool to help purge cache from Nginx

Flags:
  -h, --help       Show context-sensitive help.
      --version    Print version information and quit

Commands:
  server (s)    Run the server
  purge (p)     Purge cache now

Run "nginx-cache-purge <command> --help" for more information on a command.

$ nginx-cache-purge p --help
Usage: nginx-cache-purge purge (p) <cache-path> <key> [flags]

Purge cache now

Arguments:
  <cache-path>    Path to cache directory.
  <key>           Cache key or wildcard match.

Flags:
  -h, --help                           Show context-sensitive help.
      --version                        Print version information and quit

      --exclude-key=EXCLUDE-KEY,...    Key to exclude, can be wild card and can add multiple excludes.

$ nginx-cache-purge s --help
Usage: nginx-cache-purge server (s) [flags]

Run the server

Flags:
  -h, --help             Show context-sensitive help.
      --version          Print version information and quit

      --socket=STRING    Socket path for HTTP communication.
``
