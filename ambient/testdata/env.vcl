assert "PATH environment variable" {
    condition = strlen(env.PATH) > 0
}
