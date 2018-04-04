FROM golang
ADD . /go/src/github.com/nf/vanity
RUN go get github.com/nf/vanity
ENTRYPOINT ["/go/bin/vanity", "-http=:8080", "-https=:8443", "-anus"]
EXPOSE 8080 8443
