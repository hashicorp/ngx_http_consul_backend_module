# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

FROM golang:1.9

RUN \
  apt-get -yqq update && \
  apt-get -yqq install  \
  build-essential \
  curl \
  dnsutils \
  libpcre3 \
  libpcre3-dev \
  libssl-dev \
  unzip \
  vim \
  zlib1g-dev

RUN \
  cd /tmp && \
  curl -sLo nginx.tgz https://nginx.org/download/nginx-1.12.2.tar.gz && \
  tar -xzvf nginx.tgz

RUN \
  cd /tmp && \
  curl -sLo consul.zip https://releases.hashicorp.com/consul/1.0.0/consul_1.0.0_linux_amd64.zip && \
  unzip consul.zip && \
  mv consul /usr/local/bin/consul && \
  chmod +x /usr/local/bin/consul && \
  mkdir -p /etc/consul.d/

RUN \
  cd /tmp && \
  curl -sLo http-echo.zip https://github.com/hashicorp/http-echo/releases/download/v0.2.3/http-echo_0.2.3_linux_amd64.zip && \
  unzip http-echo.zip && \
  mv http-echo /usr/local/bin/http-echo && \
  chmod +x /usr/local/bin/http-echo

RUN \
  cd /tmp && \
  curl -sLo ndk.tgz https://github.com/simpl/ngx_devel_kit/archive/v0.3.0.tar.gz && \
  tar -xzvf ndk.tgz

ADD . /tmp/ngx_http_consul_backend_module

RUN \
  mkdir -p /usr/local/nginx/ext && \
  mkdir -p /go/src/github.com/hashicorp/ngx_http_consul_backend_module && \
  cp -R /tmp/ngx_http_consul_backend_module /go/src/github.com/hashicorp/ && \
  cd /go/src/github.com/hashicorp/ngx_http_consul_backend_module && \
  CGO_CFLAGS="-I /tmp/ngx_devel_kit-0.3.0/src" \
  go build \
    -buildmode=c-shared \
    -o /usr/local/nginx/ext/ngx_http_consul_backend_module.so \
    src/ngx_http_consul_backend_module.go

RUN \
  cd /tmp/nginx-* && \
  CFLAGS="-g -O0" \
  ./configure \
    --with-debug \
    --add-module=/tmp/ngx_devel_kit-0.3.0 \
    --add-module=/go/src/github.com/hashicorp/ngx_http_consul_backend_module \
    && \
  make && \
  make install

COPY docker/nginx.conf /usr/local/nginx/conf/nginx.conf

COPY docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 80

STOPSIGNAL SIGTERM

CMD ["/usr/local/bin/docker-entrypoint.sh"]
