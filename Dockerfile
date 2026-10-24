# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w" -a -installsuffix cgo -o main ./cmd/dedup

FROM --platform=$BUILDPLATFORM alpine:latest AS archive-tools

ARG TARGETARCH
RUN apk --no-cache add curl xz tar
COPY scripts/install-7zip.sh /install-7zip.sh
RUN sh /install-7zip.sh "$TARGETARCH" /out

FROM alpine:latest

ARG COMMIT_SHA
ARG COMMIT_MESSAGE
ARG COMMIT_TIME

RUN apk --no-cache add ca-certificates tzdata
COPY --from=archive-tools /out/bin/7zz /usr/local/bin/7zz
COPY --from=archive-tools /out/share/licenses/7zip/License.txt /usr/share/licenses/7zip/License.txt
RUN ln -s 7zz /usr/local/bin/7z
WORKDIR /app
# Copy the pre-built binary file from the previous stage
COPY --from=build /app/main ./
ENV COMMIT_SHA=${COMMIT_SHA}
ENV COMMIT_MESSAGE=${COMMIT_MESSAGE}
ENV COMMIT_TIME=${COMMIT_TIME}
CMD [ "/app/main" ]
