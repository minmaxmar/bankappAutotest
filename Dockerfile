FROM golang:1.23

WORKDIR /app


COPY go.mod .

RUN go mod download


COPY . .

# Build
ENV GOCACHE=/root/.cache/go-build
RUN --mount=type=cache,target="/root/.cache/go-build" CGO_ENABLED=0 GOOS=linux go build -v -o /bankappAutotest-api ./main.go


EXPOSE 8081

# Run
CMD ["/bankappAutotest-api"]