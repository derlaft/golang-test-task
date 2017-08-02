FROM alpine:latest

RUN mkdir /service && apk --no-cache add ca-certificates

COPY ./bin/linkfetcher /service/linkfetcher

RUN adduser -S server

USER server

EXPOSE 8080

CMD ["/service/linkfetcher"]
