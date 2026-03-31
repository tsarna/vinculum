trigger "start" "log_plugins" {
    action = loginfo("plugins", sys.plugins)
}

trigger "start" "log_features" {
    action = loginfo("features", sys.features)
}

trigger "start" "log_signals" {
    action = loginfo("signals", sys.signals)
}

