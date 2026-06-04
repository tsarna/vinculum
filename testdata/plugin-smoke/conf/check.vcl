// `vinculum check` evaluates this assert. It passes only if the plugin
// loaded and registered its function — i.e. the container plugin workflow
// works end to end for this release.
assert "plugin_loaded" {
  condition = plugin_smoke_ok()
}
