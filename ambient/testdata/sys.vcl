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

assert "sys.filepath is a string" {
    condition = strlen(sys.filepath) >= 0
}

assert "sys.writepath is a string" {
    condition = strlen(sys.writepath) >= 0
}

assert "sys.starttime is a time value" {
    condition = sys.starttime != parsetime("2000-01-01T00:00:00Z")
}

assert "sys.boottime is a time value" {
    condition = sys.boottime != parsetime("2000-01-01T00:00:00Z")
}

assert "sys.features is a list" {
    condition = length(sys.features) >= 0
}

assert "sys.signals.SIGHUP is 1" {
    condition = sys.signals.SIGHUP == 1
}

assert "sys.signals.SIGKILL is 9" {
    condition = sys.signals.SIGKILL == 9
}

assert "sys.signals.SIGSEGV is 11" {
    condition = sys.signals.SIGSEGV == 11
}

assert "sys.signals.bynumber maps 1 to SIGHUP" {
    condition = sys.signals.bynumber["1"] == "SIGHUP"
}

assert "sys.signals.bynumber maps 9 to SIGKILL" {
    condition = sys.signals.bynumber["9"] == "SIGKILL"
}

assert "sys.signals.bynumber maps 11 to SIGSEGV" {
    condition = sys.signals.bynumber["11"] == "SIGSEGV"
}
