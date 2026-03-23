assert "sys.pid is positive" {
    condition = sys.pid > 0
}

assert "sys.hostname is non-empty" {
    condition = strlen(sys.hostname) > 0
}

assert "sys.user is non-empty" {
    condition = strlen(sys.user) > 0
}

assert "sys.uid is non-negative" {
    condition = sys.uid >= 0
}

assert "sys.group is non-empty" {
    condition = strlen(sys.group) > 0
}

assert "sys.gid is non-negative" {
    condition = sys.gid >= 0
}

assert "sys.os is non-empty" {
    condition = strlen(sys.os) > 0
}

assert "sys.arch is non-empty" {
    condition = strlen(sys.arch) > 0
}

assert "sys.cpus is positive" {
    condition = sys.cpus > 0
}

assert "sys.executable is non-empty" {
    condition = strlen(sys.executable) > 0
}

assert "sys.cwd is non-empty" {
    condition = strlen(sys.cwd) > 0
}

assert "sys.homedir is non-empty" {
    condition = strlen(sys.homedir) > 0
}

assert "sys.tempdir is non-empty" {
    condition = strlen(sys.tempdir) > 0
}
