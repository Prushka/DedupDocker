# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.22-bullseye AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
RUN apt update -y
RUN apt install upx-ucl -y

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w" -a -installsuffix cgo -o main .
RUN upx --best --lzma main

FROM alpine:latest

ARG COMMIT_SHA
ARG COMMIT_MESSAGE
ARG COMMIT_TIME

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
# Copy the pre-built binary file from the previous stage
COPY --from=build /app/main ./
ENV COMMIT_SHA=${COMMIT_SHA}
ENV COMMIT_MESSAGE=${COMMIT_MESSAGE}
ENV COMMIT_TIME=${COMMIT_TIME}
CMD [ "./main" ]