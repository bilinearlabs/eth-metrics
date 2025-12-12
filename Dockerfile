FROM golang:1.25-alpine AS build

WORKDIR /app

COPY . .

RUN apk add --update gcc g++
RUN go mod download
RUN go build -o /eth-metrics

FROM golang:1.25-alpine

WORKDIR /

COPY --from=build /eth-metrics /eth-metrics

ENTRYPOINT ["/eth-metrics"]
