FROM golang
ADD . /go/src/github.com/nf/vanity
RUN go install github.com/nf/vanity
ENTRYPOINT ["/go/bin/vanity", "-http=:8080", "-anus"]
EXPOSE 8080
