server "http" "test" {
    listen = "127.0.0.1:18080"

    files "/static" {
        directory = "/tmp/static"
    }
}
