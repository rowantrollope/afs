# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY mount ./mount
RUN CGO_ENABLED=0 go build -trimpath -o /out/afs ./cmd/afs

FROM python:3.12-slim-bookworm
RUN apt-get update \
    && apt-get install -y --no-install-recommends redis-server procps \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /artifacts /lab/scripts \
    && chown 1000:1000 /artifacts
COPY --from=builder /out/afs /usr/local/bin/afs
COPY scripts/multiwriter_lab.py /lab/scripts/multiwriter_lab.py
WORKDIR /lab
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 HOME=/tmp
USER 1000:1000
ENTRYPOINT ["python3", "/lab/scripts/multiwriter_lab.py", "--binary", "/usr/local/bin/afs"]
