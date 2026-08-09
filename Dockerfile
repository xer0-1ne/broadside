# Broadside as a single container.
#
# One binary, one process, one port. There is no reverse proxy in here and no
# supervisor: if you want TLS or a domain in front of this, you almost certainly
# already run something that does that, and putting a second copy of it inside
# the container would mean running two proxies in a row for no gain.
#
# The result is a scratch image holding the binary and nothing else: no shell,
# no package manager, no libc. That is about fifteen megabytes, there is nothing
# in it to patch, and there is nothing for anyone who finds a way to execute
# code in it to execute.
#
# Nothing here needs CA certificates, because Broadside makes no outbound
# requests. Fonts are compiled in, and there is no telemetry, no update check,
# and no CDN.

# ---- Build ----------------------------------------------------------------

FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are resolved before the source is copied, so editing a Go file
# does not invalidate the module cache and re-download everything.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off means a statically linked binary with nothing to link against at
# runtime, which is what allows the scratch image below.
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -trimpath \
      -o /broadside ./cmd/broadside

# ---- Runtime --------------------------------------------------------------

FROM scratch

# Runs unprivileged. The id is numeric because scratch has no /etc/passwd for a
# name to resolve against, and 65532 is the conventional nonroot id.
#
# Whatever id this runs as has to be able to write the site volume. Override it
# with the compose file or docker run's --user; on Unraid that means 99:100.
USER 65532:65532

COPY --from=build /broadside /broadside

# Your content. This is the only path the container needs from the host.
VOLUME ["/site"]

EXPOSE 5555

# Bound to all interfaces because inside a container localhost means the
# container itself, and nothing outside could reach it.
#
# --behind-proxy is deliberately not set here. It makes Broadside trust
# X-Forwarded-For to work out a reader's real address, which is right when a
# proxy you control sits in front and wrong when anything else does, because
# then any client can forge that header and defeat the login rate limit. Turn it
# on by overriding the command; the README shows how.
ENTRYPOINT ["/broadside"]
CMD ["--site", "/site", "--listen", "0.0.0.0:5555"]
