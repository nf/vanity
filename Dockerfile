FROM golang
ADD . /go/src/anus.io/gimpy
RUN go install anus.io/gimpy
ENTRYPOINT ["/go/bin/gimpy", "-http=:8080", "-anus"]
EXPOSE 8080
