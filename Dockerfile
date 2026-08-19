# SPDX-FileCopyrightText: 2026 wagaStrim contributors
# SPDX-License-Identifier: MIT
#
# The image exists for deployments that run this next to something else on a
# server, where a tray icon means nothing and the ingest list is owned by
# whatever provisioned the machine. A desktop install does not need it.

FROM golang:1.26-bookworm AS build

WORKDIR /src

# Dependencies first, so a source edit does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# No cgo and no tray: the tray is the only package that needs either, and a
# static binary is what makes the runtime stage empty.
RUN CGO_ENABLED=0 go build -tags notray -trimpath -o /wagastrim ./cmd/wagastrim

FROM scratch

# os.UserConfigDir reads this, and there is no home directory in a scratch
# image. Mount a volume here: it holds the only mutable state the daemon keeps.
ENV XDG_CONFIG_HOME=/config
VOLUME /config

# Signaling and control are TCP, media is one UDP port. Host networking is what
# a deployment on a public IP wants, because ICE advertises the addresses it can
# see and a bridge network gives it addresses no phone can reach.
EXPOSE 7331/tcp 7332/udp 7333/tcp

COPY --from=build /wagastrim /wagastrim

ENTRYPOINT ["/wagastrim", "-headless"]
