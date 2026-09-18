# The Vinculum reference, as an MCP server
# ========================================
#
# Serves the configuration-language reference to a coding agent, so a model
# writing a .vcl stops guessing at attribute names. The reference is generated
# from the same decode structs the parser uses, so a tool answer cannot describe
# an attribute this binary cannot parse — a different guarantee from "the model
# read the manual during training".
#
# Run it against a checkout of Vinculum. The generated reference comes from the
# binary itself; --file-path is only needed for the hand-written doc/ pages:
#   vinculum serve -f /path/to/vinculum examples/man-site/
#
# Connect Claude Code / Claude Desktop by adding to your MCP config:
#   {"mcpServers": {"vcl": {"url": "http://localhost:9000/mcp"}}}
#
# Then ask: "What attributes does client \"mqtt\" take?" or "Which blocks have a
# keep_alive?"
#
# Demonstrates:
#   - man::page / man::index / man::apropos / man::synopsis — the reference as
#     data, so a config can serve its own documentation
#   - coalesce() at the call site, because a lookup that found nothing returns
#     null and a tool result must be a string. A user function cannot do this
#     for you: user functions reject null arguments
#   - a {+path} resource template, whose capture may contain slashes, where a
#     plain {path} would not match "client/mqtt" at all
#   - file()/fileset() serving the hand-written pages that the generated
#     reference deliberately does not cover — the HCL syntax itself, functy,
#     transforms
#   - an enum param, which keeps a value the action cannot handle off the wire
#   - man::check behind `disabled = !checker`: one config, whose checker is
#     switched on by an environment variable and otherwise never advertised

const {
    # Where the hand-written pages live, relative to --file-path.
    doc_dir = try(env.MAN_DOC_DIR, "doc")

    # Every generated answer says which binary it came from: an agent may be
    # targeting another release, and a confidently wrong attribute list is worse
    # than none. vcl_doc does not carry it — its pages come from the checkout
    # --file-path names, not from this binary, so the claim would not be true.
    footer = "\n\n---\nVinculum ${sys.version} — this reference describes exactly what this binary parses."

    search_advice = "Search for a word with the vcl_apropos tool, or read vcl://index for the whole map of the language."

    # Whether vcl_check is offered. Checking builds submitted text, so it is off
    # unless MAN_CHECK_PASSWORD is set — one variable that both offers the tool
    # and puts a password in front of the route carrying it (see auth.vcl). A
    # public endpoint does not even list it.
    checker = try(env.MAN_CHECK_PASSWORD, "") != ""
}

# ── Helpers ───────────────────────────────────────────────────────────────────
#
# Note what is *not* here: a helper that takes a lookup's result. User functions
# reject null arguments, and every man:: lookup returns null for "nothing is
# named that", so the coalesce() has to happen in the action itself.

# A kind is spelled as a prefix on the first word, which is what man::page
# accepts. The enum on the `kind` param keeps a misspelled kind off the wire —
# the server refuses it before the action runs. A kind written into the topic
# itself ("blok:subscription") still reaches man::page, which calls it an error;
# the actions below answer that as a miss.
function "qualified" {
    params = [topic, kind]
    result = kind == "" ? topic : "${kind}:${topic}"
}

# A page is a name, not a path. The file functions resolve a relative path
# against --file-path but do nothing to stop it climbing out, and this endpoint
# is meant to be public, so anything that is not a bare page name is refused
# here rather than opened. Every page is lower-case letters, digits and hyphens,
# so that is all the pattern admits — no "/", no ".", nothing to climb with —
# plus an optional ".md" for a caller who types the file name.
function "is_page_name" {
    params = [page]
    result = length(regexall("^[a-z0-9][a-z0-9-]{0,63}(\\.md)?$", page)) == 1
}

# The name without its extension. regex() returns the first match, which the
# pattern above guarantees is the whole name — unlike replace(), which would
# strip ".md" from the middle of one too.
function "doc_path" {
    params = [page]
    result = "${doc_dir}/${regex("^[a-z0-9-]+", page)}.md"
}

# fileset() returns names relative to its directory, so the listing is the page
# names themselves.
function "doc_index" {
    params = []
    # Only the pages the name guard admits are listed, so every name offered is
    # one vcl_doc will serve.
    result = join("\n", sort([
        for f in fileset(doc_dir, "*.md") : "  - ${regex("^[a-z0-9-]+", f)}"
        if length(regexall("^[a-z0-9][a-z0-9-]{0,63}\\.md$", f)) == 1
    ]))
}

# cond() evaluates only the branch it takes, so a rejected name never reaches
# file() and a missing page never reads one. HCL's own `a ? b : c` would
# evaluate both.
function "doc_page" {
    params = [page]
    result = cond(
        !is_page_name(page),
        "\"${page}\" is not a page name. Pages are single words:\n\n${doc_index()}",
        cond(
            fileexists(doc_path(page)),
            file(doc_path(page)),
            "There is no page named \"${page}\". Available pages:\n\n${doc_index()}"
        )
    )
}

# ── MCP server ────────────────────────────────────────────────────────────────
#
# An MCP server owns no socket: it is mounted under the `server "http"` block
# below, at /mcp. That block is where the connection is configured — the listen
# address, TLS, authentication — and its request log records every MCP call.

server "mcp" "man" {
    server_name    = "Vinculum VCL reference"
    server_version = sys.version

    # ── Tools ────────────────────────────────────────────────────────────────

    tool "vcl_man" {
        description = "Read the Vinculum configuration-language reference for one topic: a block type, one of its type variants, an attribute or sub-block under it, a ctx shape, a namespace member, a built-in function, or a vinculum command with its flags. The topic is a path, written as one space-separated string: \"subscription\", \"client mqtt\", \"server http handle\", \"send\", \"serve\". Read \"serve\" before telling anyone how to run a config: some functions exist only when a flag is given (file() needs --file-path), and a function's page says which. A name that means more than one thing answers with a menu of topic paths to call back with."

        param "topic" {
            type        = "string"
            required    = true
            description = "Topic path, space-separated: \"subscription\", \"client mqtt\", \"server http handle\", \"send\", \"serve\""
        }
        param "kind" {
            type        = "string"
            default     = ""
            enum        = ["", "block", "context", "namespace", "function", "command"]
            description = "Restrict the lookup to one kind of topic, for a name that means more than one — \"assert\" is a block type and a function, \"check\" is a block type and a command"
        }

        # A miss is ordinary text, not mcp::error(): a lookup that found nothing
        # succeeded, and an error reads to a model as "the tool broke". try()
        # makes a malformed topic — empty, or a misspelled kind — the same
        # miss, because its error would begin with this file's absolute path,
        # and this endpoint is meant to be public.
        action = "${coalesce(
            try(man::page(qualified(ctx.args.topic, ctx.args.kind)), null),
            "No topic in the Vinculum reference is named \"${ctx.args.topic}\".\n\n${search_advice}"
        )}${footer}"
    }

    tool "vcl_apropos" {
        description = "Search the Vinculum reference by keyword, when you know a word but not which block owns it. Lists every block, attribute, sub-block, ctx field, namespace member, function, command and command-line flag whose name or one-line summary contains all the keywords, each with the topic path that reads it — pass that path to vcl_man."

        param "keywords" {
            type        = "string"
            required    = true
            description = "One or more keywords, space-separated; every keyword must match, so more words narrow the search"
        }

        # try() for the reason vcl_man gives: an empty search is an error.
        action = "${coalesce(
            try(man::apropos(ctx.args.keywords), null),
            "Nothing in the Vinculum reference matches \"${ctx.args.keywords}\". The search matches substrings of names and one-line summaries, so try one word, or a shorter one."
        )}${footer}"
    }

    tool "vcl_synopsis" {
        description = "Get just the skeleton of a block — its header line, its attributes with their types and which are required, and its sub-blocks — or a function's calling conventions. Much smaller than vcl_man for a block, and the right first call before writing one. A block whose shape depends on its type label, such as \"client\", answers with the list of its types."

        param "topic" {
            type        = "string"
            required    = true
            description = "Topic path, space-separated, as for vcl_man"
        }

        # A topic with no skeleton of its own — an attribute, a ctx shape — makes
        # man::synopsis an error rather than a silence. try() falls back to the
        # whole page, which is the better answer for exactly those topics, and
        # short for them. The final null makes a malformed topic a miss, for
        # the reason vcl_man gives.
        action = "${coalesce(
            try(man::synopsis(ctx.args.topic), man::page(ctx.args.topic), null),
            "No topic in the Vinculum reference is named \"${ctx.args.topic}\".\n\n${search_advice}"
        )}${footer}"
    }

    tool "vcl_doc" {
        description = "Read one of Vinculum's hand-written documentation pages: the HCL syntax, the functy (.cty) language, transforms, testing, deployment. The generated reference describes the blocks; these pages describe the language the blocks are written in. Pass a name that does not exist to get the list of pages."

        param "page" {
            type        = "string"
            required    = true
            description = "Page name without the extension: \"functy\", \"config\", \"transforms\", \"server-http\""
        }

        action = doc_page(ctx.args.page)
    }

    # A disabled tool is not registered at all, so without MAN_CHECK_PASSWORD it
    # is absent from tools/list rather than listed and refused. Its action is
    # not evaluated then, but its description is still required.
    tool "vcl_check" {
        disabled    = !checker
        description = "Check a Vinculum configuration without running it, and get back what `vinculum check` would report: that it is valid, or each error and warning with its line quoted. Call it on what you wrote before handing it over. Pass the text of one .vcl file; it is checked alone, so a block defined in another file of the same configuration is reported as missing — pass the files joined into one. A .vinit or .cty file cannot be checked. The check sees no environment variables, so write try(env.NAME, default) rather than env.NAME for anything the deployment sets; file functions exist but read an empty directory. At most 256 KB, and ten seconds."

        param "config" {
            type        = "string"
            required    = true
            description = "The text of one .vcl file"
        }

        # man::check reports every problem in its result, never as an error,
        # so an invalid config is an ordinary answer: the check succeeded.
        action = "${man::check(ctx.args.config).text}\n---\nChecked by Vinculum ${sys.version}."
    }

    # ── Resources ────────────────────────────────────────────────────────────
    #
    # The same lookups as an attachment rather than a call: a person adding a
    # page to a conversation, rather than a model reaching for a tool mid-task.

    resource "vcl://index" {
        name        = "VCL reference index"
        description = "Every block, ctx shape and namespace of the configuration language, and every vinculum command, each with a one-line summary — the whole map of the language, in a few KB."
        mime_type   = "text/markdown"

        action = "${man::index()}${footer}"
    }

    resource "vcl://topic/{+path}" {
        name        = "VCL reference topic"
        description = "One topic of the reference, addressed by its path: vcl://topic/client/mqtt"
        mime_type   = "text/markdown"

        # {+path} is RFC 6570 reserved expansion, so the capture may contain
        # slashes and one template addresses the whole tree. A plain {path}
        # would not match "client/mqtt" at all.
        #
        # A resource has no error content of its own, so anything man::page()
        # calls an error rather than a miss — an empty path, or a misspelled
        # kind such as vcl://topic/blok:x — would reach the client as a raw
        # protocol error. An empty path is answered first (cond() never
        # evaluates the lookup), and try() turns any other error into the miss.
        action = cond(
            length(regexall("[^/ ]", ctx.args.path)) == 0,
            "Give a topic path, e.g. vcl://topic/client/mqtt. vcl://index lists them all.",
            "${coalesce(
                try(man::page(replace(ctx.args.path, "/", " ")), null),
                "No topic in the Vinculum reference is named \"${replace(ctx.args.path, "/", " ")}\"."
            )}${footer}"
        )
    }

    # ── Prompt ───────────────────────────────────────────────────────────────

    prompt "write_vcl" {
        description = "Ground a model in how to look the configuration language up before writing it."

        param "task" {
            type        = "string"
            default     = ""
            description = "What the configuration should do"
        }

        # A bare string is a single message from the user; mcp::user_message()
        # and mcp::assistant_message() are for controlling roles.
        action = <<-EOT
            You are writing a Vinculum configuration (.vcl). Look the language up rather than recalling it: these tools answer from the binary that will parse the file, so they cannot describe an attribute it would reject.

            Work in this order:
              1. vcl_apropos with a keyword, when you know what you want but not which block does it.
              2. vcl_synopsis on the block you settled on, for its shape and its required attributes.
              3. vcl_man on the block, or on one attribute of it, for the detail and the ctx an expression sees.
              4. vcl_doc for the language the blocks are written in — "config" for the HCL syntax, "functy" for .cty, "transforms", "testing".

            Then check what you wrote with ${checker ? "the vcl_check tool" : "`vinculum check <file>`"}, and read vcl_man "serve" for how to run it: some functions do not exist unless a flag is given — file() needs --file-path — and a function's page says which.${ctx.args.task == "" ? "" : "\n\nThe task: ${ctx.args.task}"}
        EOT
    }
}

# ── HTTP server ───────────────────────────────────────────────────────────────
#
# The route has no trailing slash: Streamable HTTP uses a single endpoint, and an
# exact "/mcp" match is what clients expect. A trailing-slash "/mcp/" would be a
# subtree match, so a client connecting to ".../mcp" would be redirected (307)
# and may fail.

server "http" "main" {
    listen = try(env.MAN_LISTEN, ":9000")

    # The policy everything inherits, including whatever the site grows into.
    # A route may replace it, and /mcp does when there is a checker behind it.
    # An HCL ternary evaluates both branches, but each is a bare reference, so
    # cond() is not needed. See auth.vcl for why this is not a const.
    auth = try(env.MAN_PASSWORD, "") == "" ? auth.anonymous : auth.site

    handle "/mcp" {
        handler = server.man

        # The site's policy until there is a checker behind this route, and the
        # checker's from then on. Turning the checker on therefore closes the
        # reference *over MCP*: the tools of one `server "mcp"` block are one
        # list, so a second endpoint serving the reference anonymously would
        # mean a second copy of every tool. The HTTP site keeps its own policy.
        auth = checker ? auth.checker : (
            try(env.MAN_PASSWORD, "") == "" ? auth.anonymous : auth.site
        )
    }
}
