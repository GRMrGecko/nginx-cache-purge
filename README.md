# nginx-cache-purge
A tool to help purge Nginx cache. It can either run locally with the purge command, or run as a local unix service to allow for purging by Nginx http requests.

## Install
You can install either by downloading the latest binary release, or by building.

## Building
Building should be as simple as running:
```
go build
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
    proxy_cache_key $host$request_uri;

    server {
        location / {
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$host$request_uri break;
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
    proxy_cache_key $host$request_uri;

    server {
        location / {
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$host$request_uri break;
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
    proxy_cache_key $host$request_uri;

    server {
        location / {
            if ($is_purge) {
                proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
                rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$host$request_uri break;
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
    proxy_cache_key $host$request_uri;

    server {
        location / {
            proxy_cache my_cache;
            proxy_pass http://upstream;
        }
        location ~ /purge(/.*) {
            allow 127.0.0.1;
            deny all;
            proxy_pass http://unix:/var/run/nginx-cache-purge/http.sock;
            rewrite ^ /?path=/var/nginx/proxy_temp/cache&key=$host$1 break;
        }
    }
}
```
