package config

import (
	"fmt"
	"sort"
)

// Renames: what a name used to be, and what it became.
//
// The deferred-reference checker already offers a nearest-match suggestion for
// an unknown name, and for a rename whose spelling barely moved that is enough
// — `randint` is one edit from `rand::int`, and the guess is right. The table
// here exists for the renames edit distance cannot reach, which is most of the
// ones where the *leaf name* changed rather than just the separator: `now`,
// `sunrise`, `nextzoneserial`, `basicauth`, `serialize`. For those the checker
// said nothing at all, and for a few it said something worse than nothing —
// `now()` was answered "Did you mean pow?" and `since()` "Did you mean slice?",
// which sends an upgrading author to look at the wrong function.
//
// So the rule for what belongs here: a rename the suggester does not already
// find, or one it finds wrongly. Adding an entry the suggester handles is not
// harmful — a stated fact beats a guess — but it is bulk, and bulk is what
// stops a table like this from being maintained. What must never happen is an
// entry that misdirects, which is what RenameProblems is for.
//
// Only functions and `ctx` fields are covered, the two things the reference
// checker resolves by name. Renaming a block *attribute* is not reported here:
// gohcl rejects an unknown attribute during block decoding, long before this
// runs, so that would need an interception point of its own.

// rename records what one removed name became.
type rename struct {
	// now is the name to use instead, or empty when the thing was removed with
	// no single replacement — note then has to carry the explanation.
	now string
	// since is the release the change landed in.
	since string
	// note is an optional sentence appended to the diagnostic, for a change
	// that needs more than "X is now Y".
	note string
}

// ctxFieldName identifies one field of one `ctx` shape. A field is renamed out
// of a particular shape, never globally: `ctx.topic` is gone from the shape a
// receiver's `vinculum_topic` sees, and entirely correct in the `message` shape
// that a subscription's `action` sees.
type ctxFieldName struct {
	shape string
	field string
}

// renamedCtxFields records fields removed from a `ctx` shape.
var renamedCtxFields = map[ctxFieldName]rename{
	{"inbound-message", "topic"}: {
		since: "0.46.0",
		note: "A receiver's vinculum_topic expression exists to produce the bus topic, " +
			"so there is no bus topic in scope while it runs. The transport's own " +
			"identifier is named after the transport instead, and is one of the " +
			"fields above.",
	},
}

// renamedFunctions records functions that changed name.
//
// Every entry below is from the 0.43.0 namespacing of vinculum's own function
// families. The ones that release renamed by inserting `::` alone — `randint`
// to `rand::int`, `log_info` to `log::info`, `geo_point` to `geo::point` — are
// deliberately absent: the suggester finds those unaided, and listing them
// would double the table without changing a single diagnostic.
var renamedFunctions = map[string]rename{
	// inbound:: — settling an inbound delivery stopped being per-protocol.
	//
	// These are the reason the function branch is consulted before the
	// namespace branch. Removing them empties `redis::` and `sqs::` entirely,
	// and the namespaced branch's nearest-match search only looks *within* a
	// namespace — so without an entry here an upgrading configuration is told
	// "there are no functions in namespace redis::" and left to guess.
	"redis::ack": {
		now: "inbound::ack", since: "0.46.0",
		note: "It takes no consumer and no entry ID: the delivery being settled rides on " +
			"ctx, which is what lets the same expression work from a subscription " +
			"several bus hops away. Set ack = \"manual\" on the consumer.",
	},
	"sqs::delete": {
		now: "inbound::ack", since: "0.46.0",
		note: "It takes no receiver and no receipt handle: the delivery being settled " +
			"rides on ctx, which is what lets the same expression work from a " +
			"subscription several bus hops away. Set ack = \"manual\" on the receiver.",
	},
	"sqs::extend_visibility": {
		now: "inbound::keepalive", since: "0.46.0",
		note: "It takes no receiver, no receipt handle, and no timeout: the delivery is " +
			"read from ctx and extended by the visibility window the queue actually " +
			"uses.",
	},

	// time:: — the leaf name dropped the word "time", or gained a word.
	"now":        {now: "time::now", since: "0.43.0"},
	"parsetime":  {now: "time::parse", since: "0.43.0"},
	"formattime": {now: "time::format", since: "0.43.0"},
	"since":      {now: "time::since", since: "0.43.0"},
	"until":      {now: "time::until", since: "0.43.0"},
	"fromunix":   {now: "time::from_unix", since: "0.43.0"},
	"unix":       {now: "time::to_unix", since: "0.43.0"},
	"intimezone": {now: "time::in_zone", since: "0.43.0"},
	"addyears":   {now: "time::add_years", since: "0.43.0"},
	"addmonths":  {now: "time::add_months", since: "0.43.0"},
	"adddays":    {now: "time::add_days", since: "0.43.0"},
	"strftime":   {now: "time::strftime", since: "0.43.0"},
	"strptime":   {now: "time::strptime", since: "0.43.0"},

	// duration::
	"formatduration": {now: "duration::format", since: "0.43.0"},
	"absduration":    {now: "duration::abs", since: "0.43.0"},
	"durationlt":     {now: "duration::lt", since: "0.43.0"},
	"durationgt":     {now: "duration::gt", since: "0.43.0"},

	// dns:: — never time functions, and given a namespace of their own.
	"nextzoneserial":  {now: "dns::next_zone_serial", since: "0.43.0"},
	"parsezoneserial": {now: "dns::parse_zone_serial", since: "0.43.0"},

	// sky:: — the solar and celestial half of the geo functions.
	"sunrise":        {now: "sky::sunrise", since: "0.43.0"},
	"sunset":         {now: "sky::sunset", since: "0.43.0"},
	"solar_noon":     {now: "sky::solar_noon", since: "0.43.0"},
	"solar_midnight": {now: "sky::solar_midnight", since: "0.43.0"},
	"sun_position":   {now: "sky::sun_position", since: "0.43.0"},
	"moon_position":  {now: "sky::moon_position", since: "0.43.0"},
	"moon_phase":     {now: "sky::moon_phase", since: "0.43.0"},

	// rand:: — a leaf name should not repeat its namespace, and this one said
	// what it returns instead.
	"random": {now: "rand::float", since: "0.43.0",
		note: "The leaf name says what it returns: a float in [0.0, 1.0)."},

	// url::
	"urljoinpath":    {now: "url::join_path", since: "0.43.0"},
	"urlqueryencode": {now: "url::query_encode", since: "0.43.0"},
	"urlquerydecode": {now: "url::query_decode", since: "0.43.0"},

	// http:: — the run-together names gained underscores as they were namespaced.
	"addheader":    {now: "http::add_header", since: "0.43.0"},
	"removeheader": {now: "http::remove_header", since: "0.43.0"},
	"setcookie":    {now: "http::set_cookie", since: "0.43.0"},
	"basicauth":    {now: "http::basic_auth", since: "0.43.0"},

	// mcp::
	"mcp_usermessage":      {now: "mcp::user_message", since: "0.43.0"},
	"mcp_assistantmessage": {now: "mcp::assistant_message", since: "0.43.0"},

	// wire:: — these three had no prefix to drop, so the namespace is all new.
	"serialize":    {now: "wire::serialize", since: "0.43.0"},
	"serializestr": {now: "wire::serialize_str", since: "0.43.0"},
	"deserialize":  {now: "wire::deserialize", since: "0.43.0"},

	// send:: — bare `send` keeps its name, which is why the suggester offers it
	// here and is wrong.
	"sendgo": {now: "send::go", since: "0.43.0"},
}

// verdict is what happened to the name, in one sentence. It opens with "It"
// because the name has just been quoted by the sentence before it, and
// repeating it there reads like two separate reports of one mistake.
func (r rename) verdict() string {
	if r.now == "" {
		return fmt.Sprintf("It was removed in %s.", r.since)
	}
	return fmt.Sprintf("It was renamed %q in %s.", r.now, r.since)
}

// describe is the verdict with its note, for a diagnostic that has nothing to
// say between them. A caller that does — the ctx-field check lists the fields
// the context has — composes the two itself, so the explanation lands after the
// facts it explains rather than before them.
func (r rename) describe() string {
	if r.note == "" {
		return r.verdict()
	}
	return r.verdict() + " " + r.note
}

// RenameProblems reports entries in the rename tables that no longer match the
// language: an old name that has come back, a replacement that does not exist,
// a context shape that is gone, or a field renamed out of a shape that still
// has it.
//
// A rename table earns its place by being right. A wrong entry is worse than no
// entry, because it is stated as fact and it silences the suggester that would
// otherwise have guessed correctly — so this exists to fail the build rather
// than misdirect an upgrade. `timeadd` is the live example of the first case:
// it was renamed `time::add` in 0.43.0 and then handed back to cty's own
// stdlib, so it is a working function again and must not be listed.
//
// Exported because only a package linking every function plugin can answer the
// question, and that is not this one.
func (c *Config) RenameProblems() []string {
	var problems []string

	available := c.possibleFunctionNames()
	for old, r := range renamedFunctions {
		if available[old] {
			problems = append(problems, fmt.Sprintf(
				"renamed function %q exists again; remove it from the table", old))
		}
		if r.now != "" && !available[r.now] {
			problems = append(problems, fmt.Sprintf(
				"renamed function %q points at %q, which does not exist", old, r.now))
		}
		if r.now == "" && r.note == "" {
			problems = append(problems, fmt.Sprintf(
				"removed function %q has no replacement and no note explaining it", old))
		}
	}

	doc, _ := GenerateSchema(SchemaGenOptions{})
	if doc == nil {
		return problems
	}
	for key, r := range renamedCtxFields {
		shape := doc.Contexts[key.shape]
		if shape == nil {
			problems = append(problems, fmt.Sprintf(
				"renamed ctx field %q names context %q, which does not exist", key.field, key.shape))
			continue
		}
		var hasOld, hasNew bool
		for _, f := range shape.Fields {
			hasOld = hasOld || f.Name == key.field
			hasNew = hasNew || f.Name == r.now
		}
		if hasOld {
			problems = append(problems, fmt.Sprintf(
				"ctx field %q is listed as renamed out of context %q, which still has it",
				key.field, key.shape))
		}
		// A replacement is checked only when it is a field of the shape itself.
		// A shape with OpenFields is completed per site — which is the whole
		// reason the one entry here has no single replacement — and a site's
		// additions are not knowable from the shape alone.
		if r.now != "" && !hasNew && !shape.OpenFields {
			problems = append(problems, fmt.Sprintf(
				"renamed ctx field %q points at %q, which context %q does not have",
				key.field, r.now, key.shape))
		}
		if r.now == "" && r.note == "" {
			problems = append(problems, fmt.Sprintf(
				"removed ctx field %q in context %q has no replacement and no note explaining it",
				key.field, key.shape))
		}
	}

	sort.Strings(problems)
	return problems
}
