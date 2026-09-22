# syntax=docker/dockerfile:1

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the freshly built frontend instead of whatever is committed.
RUN rm -rf internal/web/dist && mkdir -p internal/web/dist
COPY --from=web /src/web/dist/ internal/web/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/conclave ./cmd/conclave

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/conclave /usr/local/bin/conclave
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/conclave"]
CMD ["serve", "--addr", "0.0.0.0:8080"]
