FROM golang:1.25-alpine AS build

WORKDIR /usr/src/app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
COPY go.mod ./
RUN go mod download && go mod verify

COPY . .
RUN go build -v -o /usr/local/bin/app ./main.go

FROM alpine:3.23 AS mimetypes

RUN apk add --no-cache apache2

FROM alpine:3.23

# Make more mimetypes available to https://pkg.go.dev/mime#TypeByExtension
COPY --from=mimetypes /etc/apache2/mime.types /etc/apache2/mime.types
COPY --from=build /usr/local/bin/app /usr/local/bin/app

CMD ["app"]
