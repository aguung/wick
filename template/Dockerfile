FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
RUN apk add --no-cache git bash
COPY . .
RUN go install github.com/yogasw/wick@latest
RUN wick setup
# --headless drops the systray UI (container has no display) and
# --target picks up the docker buildx target platform so multi-arch
# images (linux/amd64, linux/arm64) build without extra config.
# gate binary is embedded inside app by wick build (assets/gate-<os>-<arch>).
RUN wick build --headless --target ${TARGETOS}/${TARGETARCH} -o /out/app

# Build gotty for the target platform (cross-compile)
RUN mkdir -p /out && \
    cd /tmp && \
    git clone --depth 1 --branch v1.5.0 https://github.com/sorenisanerd/gotty.git && \
    cd gotty && \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/gotty .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        wget \
        git \
        sudo \
        vim \
        procps \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL https://claude.ai/install.sh | CLAUDE_INSTALL_DIR=/usr/local/bin bash

# Non-root user — claude CLI writes credentials to ~/.claude/
RUN useradd -m -u 1001 -s /bin/bash app && \
    echo "app ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/app && \
    chmod 440 /etc/sudoers.d/app

COPY --from=build /out/app   /usr/local/bin/app
COPY --from=build /out/gotty /usr/local/bin/gotty

RUN chmod 555 /usr/local/bin/app /usr/local/bin/gotty

USER app
WORKDIR /home/app

EXPOSE 9425

ENTRYPOINT ["/usr/local/bin/app"]
CMD ["server"]
