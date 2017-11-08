# nginx + Consul Backend

This repository contains an [nginx](https://nginx.org) module extension for
dynamically choosing a healthy backend by communicating directly with HashiCorp
[Consul](https://www.consul.io/)'s API.

**This code is for example purposes only. It demonstrates both the ability to
call Go code from C and the ability to link nginx directly to Consul! It is
not production ready and should be considered _inspiration only_.**


## Usage

This module installs a `consul` directive inside the `location` block, and sets
the resulting `$backend` variable to one of the healthy IP:PORT by the given
service.

```nginx
http {
  server {
    listen       80;
    server_name  example.com;

    location /my-service {
      consul $backend service-name;
      proxy_pass http://$backend;
    }
  }
}
```

In this example, when a request to "http://example.com/my-service" comes to
nginx, nginx invokes the `consul` directive and looks for a healthy service
named "service-name", returning a random entry from the list. Then it utilized
nginx's built-in `proxy_pass` to send traffic to that IP:PORT.

To put it another way, requests to "http://example.com/my-service" are
load-balanced among the instances registered in a Consul service, using Consul's 
health checks to remove unhealthy backends automatically.


## Architecture

Instead of re-inventing the wheel, this module uses Consul's existing [Golang
API client](https://github.com/hashicorp/consul/tree/master/api). The majority
of the code is written in Go, with small glue pieces to wire it back into the
required C code for nginx. This makes use of [golang shared C
libraries](http://blog.ralch.com/tutorial/golang-sharing-libraries/).

Positively, this gains all the benefits of using the official API client
library, including configuration via familiar environment variables, connection
pooling and multiplexing, and time-tested stability. This saves the need to
write and maintain a new client library written in pure C, and allows us to
showcase a really cool feature of Go - shared C libraries. Each request goes
through Consul, meaning the probability of routing traffic to an unhealthy host
is significantly lower than other solutions.

Negatively, this requires Golang to compile the dynamic library `.so` file. In
theory, this could be compiled in advance by a CI/CD system. There is no need
for the Golang _runtime_, since the runtime is compiled into the dynamic library.
Additionally, each request goes through Consul. Thus using a local agent is
required for performance and latency reasons.

The general flow is as follows:

1. A request comes into nginx that matches a defined `location` block with a
`consul` directive.

1. nginx calls the `ngx_http_consul_backend` function with two arguments.

  1. The first argument is the variable in which to store the result
  (e.g. `$backend`).

  1. The second argument is the name of the Consul service to route to
  (e.g. `my-service`).

1. The `ngx_http_consul_backend` calls `dlopen` on the shared C library (the
`.so` file mentioned above), and executes the Go function by calling its symbol.

1. The Go function communicates with Consul using the official API client
library, compiles a list of IP:PORT results, and then chooses a random result to
return.

1. The IP:PORT is returned to the `ngx_http_consul_backend` function, which then
sets the result as the defined variable (e.g. `$backend`).

1. Usually the next step is to use the built-in `proxy_pass` directive to send
traffic to that host.

## Installation

This installation guide uses ubuntu/debian. Adapt as-needed for other platforms.

### Prerequisites

- [Golang](https://golang.org) >= 1.9
- Standard build tools, including GCC

### Steps

1. Install the necessary build tools:

    ```sh
    $ apt-get -yqq install build-essential curl git libpcre3 libpcre3-dev libssl-dev zlib1g-dev
    ```

1. Download and extract nginx source:

    ```sh
    $ cd /tmp
    $ curl -sLo nginx.tgz https://nginx.org/download/nginx-1.12.2.tar.gz
    $ tar -xzvf nginx.tgz
    ```

1. Download and extract the nginx development kit (ndk):

    ```sh
    $ cd /tmp
    $ curl -sLo ngx_devel_kit-0.3.0.tgz https://github.com/simpl/ngx_devel_kit/archive/v0.3.0.tar.gz
    $ tar -xzvf ngx_devel_kit-0.3.0.tgz
    ```

1. Download/clone this repository:

    ```sh
    $ git clone https://github.com/hashicorp/ngx_http_consul_backend_module.git /go/src/github.com/hashicorp/ngx_http_consul_backend_module
    ```

1. Compile the Go code as a shared C library which nginx will dynamically load.
This uses CGO and binds to the nginx development kit:

    ```sh
    $ cd /tmp/ngx_http_consul_backend_module/src
    $ mkdir -p /usr/local/nginx/ext
    $ CGO_CFLAGS="-I /tmp/ngx_devel_kit-0.3.0/src" \
        go build \
          -buildmode=c-shared \
          -o /usr/local/nginx/ext/ngx_http_consul_backend_module.so \
          src/ngx_http_consul_backend_module.go
    ```

    This will compile the object file with symbols to
    `/usr/local/nginx/ext/nginx_http_consul_backend_module.so`. Note that the
    name and location of this file is important - it will be `dlopen`ed at
    runtime by nginx.

1. Compile and install nginx with the modules:

    ```sh
    $ cd /tmp/nginx-1.12.2
    $ CFLAGS="-g -O0" \
        ./configure \
          --with-debug \
          --add-module=/tmp/ngx_devel_kit-0.3.0 \
          --add-module=/go/src/github.com/hashicorp/ngx_http_consul_backend_module
    $ make
    $ make install
    ```

1. Add the required nginx configuration and restart nginx:

    ```nginx
    http {
      server {
        listen       80;
        server_name  example.com;

        location /my-service {
          consul $backend service-name;
          proxy_pass http://$backend;
        }
      }
    }
    ```

    Unlike other solutions, you will not have to restart nginx each time a
    change happens in the Consul services. Instead, because each request
    delegates to Consul, you will get real-time results, and traffic will never
    be routed to an unhealthy host!

## Development

There is a sample Dockerfile and entrypoint which builds and runs this custom
nginx installation with all required modules.

## Alternatives

- [Consul Template](https://github.com/hashicorp/consul-template)
- [nginx upstream sync](https://github.com/weibocom/nginx-upsync-module)
