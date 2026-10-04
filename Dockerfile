# 在容器里跑 Linux 版。
#
# 用途有限，先说清楚：这个工具是给 Windows 桌面用的，靠内嵌 WebView2 显示界面。
# 容器里没有图形界面，只能以 --no-open 起服务，再从宿主机浏览器访问打印出来的地址。
# 那条路要求宿主机是 Linux 且用 --network host（服务只监听容器内的回环地址）。
#
# 见 README 的「Docker」一节。
#
# 用法：
#   docker build -t web-ics-ca .
#   docker run --rm --network host -v "$PWD/ics-certs:/certs" web-ics-ca --out-dir /certs
#   docker logs <容器名>          # 里面那行带令牌的地址复制到宿主机浏览器打开

FROM golang:1.25-alpine AS build
WORKDIR /src

# 先只拷依赖清单，让依赖层能被缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w -X main.version=docker" -o /out/web-ics-ca .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
# 让 GHCR 把镜像包与本仓库关联；已有包还需要在包设置里给 Actions 授权。
LABEL org.opencontainers.image.source="https://github.com/loveFirefly-26710/Web_ICS_CA"
COPY --from=build /out/web-ics-ca /usr/local/bin/web-ics-ca

# 证书写到这里，运行时用 -v 挂出来
WORKDIR /certs

# 容器里没有图形界面，固定 --no-open
ENTRYPOINT ["/usr/local/bin/web-ics-ca", "--no-open"]
