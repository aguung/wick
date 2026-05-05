FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git bash
COPY . .
RUN go install github.com/yogasw/wick@latest
RUN wick setup
RUN wick build -o /out/app

FROM alpine:3.19
WORKDIR /app
COPY --from=build /out/app /app/app
EXPOSE 9425
ENTRYPOINT ["/app/app"]
CMD ["server"]
