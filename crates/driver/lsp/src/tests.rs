use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;

use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use lsp_server::Connection;
use lsp_server::Message;
use lsp_server::Notification;
use lsp_server::Request;
use lsp_server::RequestId;
use lsp_server::Response;
use lsp_types::DiagnosticSeverity;
use lsp_types::NumberOrString;
use lsp_types::Position;
use lsp_types::PublishDiagnosticsParams;
use lsp_types::Url;
use serde_json::json;
use source::SourceFile;
use source::Span;
use syntax::operators::BinaryOp;
use syntax::operators::UnaryOp;

use crate::DEBOUNCE;
use crate::OpenFile;
use crate::checked::CachedKind;
use crate::convert::lsp_diagnostics;
use crate::convert::offset;
use crate::convert::position;
use crate::serve_connection;
use crate::server::Server;

/// The request params of the place of `uri`'s `text` at the `nth` `needle`:
/// at the character it underlines with U+0332, else at its start.
/// `"x - -̲1.0"` is the second `-`.
fn place(uri: &Url, text: &str, needle: &str, nth: usize) -> serde_json::Value {
    const UNDERLINE: char = '\u{332}';
    let underlined = needle.find(UNDERLINE).map_or(0, |mark| {
        needle[..mark].char_indices().last().map_or(0, |(at, _)| at)
    });
    let found = needle.replace(UNDERLINE, "");
    let start = text
        .match_indices(&found)
        .nth(nth)
        .unwrap_or_else(|| panic!("no {nth} {found:?}"))
        .0;
    let position = position(&SourceFile::new("t.peep", text), start + underlined);
    json!({ "textDocument": { "uri": uri }, "position": position })
}

/// A file named from the root, as an editor names files; on Windows that
/// needs a drive.
#[cfg(unix)]
const MAIN_FILE: &str = "/app/main.peep";
#[cfg(windows)]
const MAIN_FILE: &str = "C:/app/main.peep";

#[test]
fn positions_count_utf16_units() {
    // Line 2 is "çd𝄞x": `ç` is 2 bytes and 1 unit, `𝄞` 4 bytes and 2 units.
    let file = SourceFile::new("t.peep", "a\nçd𝄞x\r\nlast");
    for (offset, line, character) in [
        (0, 0, 0),
        (2, 1, 0),
        (4, 1, 1),
        (5, 1, 2),
        (7, 1, 2), // inside `𝄞`: counted up to its start
        (9, 1, 4),
        (10, 1, 5),
        (12, 2, 0),
        (16, 2, 4),
    ] {
        assert_eq!(
            position(&file, offset),
            Position::new(line, character),
            "offset {offset}"
        );
        if offset != 7 {
            let back = crate::convert::offset(&file, Position::new(line, character));
            assert_eq!(back, offset, "back from {line}:{character}");
        }
    }
    assert_eq!(
        offset(&file, Position::new(1, 99)),
        10,
        "past the line's end"
    );
}

#[test]
fn converts_labels_and_notes() {
    let file = Arc::new(SourceFile::new(MAIN_FILE, "let x = 1;\nx = 2;\n"));
    let diagnostic = Diagnostic::new(diagnostics::UNREACHABLE_CODE, "unreachable code")
        .label(&file, Span::new(11, 16), "this never runs")
        .label(&file, Span::new(4, 5), "declared here")
        .label(&file, Span::new(4, 5), "")
        .label(&file, Span::new(4, 5), "used later here")
        .note("a note")
        .help("a help line", Vec::new());
    let converted = lsp_diagnostics(&diagnostic, &HashMap::new(), true);
    let [converted, hint] = converted.as_slice() else {
        panic!("one diagnostic and one hint: {converted:?}");
    };
    assert_eq!(converted.range.start, Position::new(1, 0));
    assert_eq!(converted.range.end, Position::new(1, 5));
    assert_eq!(converted.severity, Some(DiagnosticSeverity::WARNING));
    assert_eq!(
        converted.code,
        Some(NumberOrString::String("W0001".to_string()))
    );
    assert_eq!(
        converted.message,
        "unreachable code\nthis never runs\nnote: a note\nhelp: a help line"
    );
    let related = converted.related_information.as_ref().unwrap();
    assert_eq!(related.len(), 1, "labels on one span are one entry");
    assert_eq!(related[0].message, "declared here, used later here");
    assert_eq!(hint.severity, Some(DiagnosticSeverity::HINT));
    let shown = (&hint.message, hint.range);
    assert_eq!(shown, (&related[0].message, related[0].location.range));
    assert_eq!(
        (&hint.code, &hint.source),
        (&converted.code, &converted.source)
    );
    let back = &hint.related_information.as_ref().unwrap()[0];
    assert_eq!(
        back.location.range, converted.range,
        "points back to the error"
    );
    assert_eq!(related[0].location.range.start, Position::new(0, 4));
    assert_eq!(
        related[0].location.uri,
        Url::from_file_path(MAIN_FILE).unwrap()
    );
}

/// A help line's fixes are published in the diagnostic's `data` and come
/// back as one quick fix when the editor asks for code actions there,
/// versioned when the editor takes that.
#[test]
fn help_fixes_become_quick_fixes() {
    let file = Arc::new(SourceFile::new(MAIN_FILE, "let a = cout;\n"));
    let at = Span::new(8, 12);
    let fix = Fix {
        file: file.clone(),
        span: at,
        new_text: "count".to_string(),
    };
    let diagnostic = Diagnostic::new(diagnostics::UNDEFINED_NAME, "unknown name `cout`")
        .label(&file, at, "")
        .guess("did you mean `count`?", vec![fix])
        .help("a help line without a fix", Vec::new());
    let open = OpenFile {
        text: file.text.clone(),
        version: 7,
    };
    let open_files = HashMap::from([(PathBuf::from(MAIN_FILE), open)]);
    let published = lsp_diagnostics(&diagnostic, &open_files, true).remove(0);
    let uri = Url::from_file_path(MAIN_FILE).unwrap();
    let params = json!({
        "textDocument": { "uri": uri },
        "range": published.range,
        "context": { "diagnostics": [published] },
    });
    let (connection, _client) = Connection::memory();
    let server = Server::new(&connection, Path::new("no-libraries"), true);
    let Response { result, error, .. } =
        server.answer_code_actions(RequestId::from(1), params.clone());
    assert!(error.is_none(), "{error:?}");
    let result = result.unwrap();
    let document = &result[0]["edit"]["documentChanges"][0];
    assert_eq!(document["textDocument"]["uri"], uri.as_str());
    assert_eq!(document["textDocument"]["version"], 7, "made for this text");
    let edits = &document["edits"];
    assert_eq!(result.as_array().map(Vec::len), Some(1));
    assert_eq!(result[0]["title"], "did you mean `count`?");
    assert_eq!(result[0]["kind"], "quickfix");
    assert_eq!(result[0]["isPreferred"], false, "a guess");
    assert_eq!(result[0]["diagnostics"][0]["message"], published.message);
    assert_eq!(
        edits,
        &json!([{ "range": { "start": { "line": 0, "character": 8 }, "end": { "line": 0, "character": 12 } }, "newText": "count" }])
    );
    // An editor that takes no versioned edits gets none for text not
    // checked, as this file is not; else it gets them in `changes`.
    let unchecked = Server::new(&connection, Path::new("no-libraries"), false);
    let refused = unchecked.answer_code_actions(RequestId::from(2), params);
    assert_eq!(refused.result, Some(json!([])));
    let unversioned = lsp_diagnostics(&diagnostic, &open_files, false).remove(0);
    let edit = &unversioned.data.unwrap()[0]["edit"];
    assert_eq!(edit.get("documentChanges"), None);
    assert_eq!(&edit["changes"][uri.as_str()], edits);
    let plain = Diagnostic::new(diagnostics::UNDEFINED_NAME, "no help");
    let plain = lsp_diagnostics(&plain, &open_files, true).remove(0);
    assert_eq!(plain.data, None, "nothing to fix, nothing in `data`");
}

/// Opening a file with an error publishes it; changing the open text to
/// fix it clears it, though the file on disk still has the error.
#[test]
fn publishes_and_clears_diagnostics() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    let file = folder.join("main.peep");
    let broken = "fn main() { a $ b; }\n";
    std::fs::write(&file, broken).unwrap();
    let uri = Url::from_file_path(&file).unwrap();

    let (server, client) = Connection::memory();
    let thread =
        std::thread::spawn(move || serve_connection(server, Path::new("no-libraries")).unwrap());
    let send = |message: Message| client.sender.send(message).unwrap();
    let request = |id: i32, method: &str, params: serde_json::Value| {
        Message::Request(Request::new(
            RequestId::from(id),
            method.to_string(),
            params,
        ))
    };
    let notification = |method: &str, params: serde_json::Value| {
        Message::Notification(Notification::new(method.to_string(), params))
    };
    let published = || match client.receiver.recv().unwrap() {
        Message::Notification(notification)
            if notification.method == "textDocument/publishDiagnostics" =>
        {
            serde_json::from_value::<PublishDiagnosticsParams>(notification.params).unwrap()
        }
        other => panic!("not a publish: {other:?}"),
    };

    send(request(1, "initialize", json!({ "capabilities": {} })));
    let Message::Response(response) = client.receiver.recv().unwrap() else {
        panic!("no initialize response");
    };
    let result = response.result.unwrap();
    assert_eq!(result["capabilities"]["textDocumentSync"], json!(1));
    send(notification("initialized", json!({})));

    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": broken });
    send(notification(
        "textDocument/didOpen",
        json!({ "textDocument": document }),
    ));
    let opened = published();
    assert_eq!(opened.uri, uri);
    let first = &opened.diagnostics[0];
    assert_eq!(
        first.code,
        Some(NumberOrString::String("L0001".to_string()))
    );
    assert_eq!(first.range.start, Position::new(0, 14));

    // Three quick changes: one check, after the pause, of the last text.
    let change = |version: i32, text: &str| {
        let change = json!({
            "textDocument": { "uri": uri, "version": version },
            "contentChanges": [{ "text": text }],
        });
        notification("textDocument/didChange", change)
    };
    send(change(2, "fn main() { a $ }\n"));
    send(change(3, "fn main() { $ }\n"));
    send(change(4, "fn main() {}\n"));
    let typed = Instant::now();
    let changed = published();
    assert!(typed.elapsed() >= DEBOUNCE, "checked before the pause");
    assert_eq!(changed.uri, uri);
    assert!(changed.diagnostics.is_empty(), "{:?}", changed.diagnostics);
    let more = client.receiver.recv_timeout(Duration::from_millis(50));
    assert!(more.is_err(), "a second publish: {more:?}");

    send(request(2, "shutdown", serde_json::Value::Null));
    assert!(matches!(
        client.receiver.recv().unwrap(),
        Message::Response(_)
    ));
    send(notification("exit", serde_json::Value::Null));
    thread.join().unwrap();
}

/// Asked for `source.fixAll` only, as on save, the server answers the
/// open file's sure fixes as one action made for its version, and no
/// quick fix, though the diagnostics it names carry the `count` guess.
#[test]
fn fixes_all_sure_problems() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-fix-all-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    let file = folder.join("main.peep");
    let text = "fn main() {\n    let count = 1\n    let a = cout\n}\n";
    std::fs::write(&file, text).unwrap();
    let uri = Url::from_file_path(&file).unwrap();
    let (connection, client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 3, "text": text });
    let open = json!({ "textDocument": document });
    let open = Notification::new("textDocument/didOpen".to_string(), open);
    server.apply_notification(open).unwrap();
    let Ok(Message::Notification(published)) = client.receiver.try_recv() else {
        panic!("the open did not publish");
    };
    let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
    assert_eq!(published.diagnostics.len(), 3, "two `;` and `cout`");
    let params = json!({
        "textDocument": { "uri": uri },
        "range": { "start": { "line": 0, "character": 0 }, "end": { "line": 4, "character": 0 } },
        "context": { "diagnostics": published.diagnostics, "only": ["source.fixAll"] },
    });
    let ask = Request::new(
        RequestId::from(1),
        "textDocument/codeAction".to_string(),
        params,
    );
    let result = server.answer_request(ask).result.unwrap();
    assert_eq!(result.as_array().map(Vec::len), Some(1), "{result}");
    assert_eq!(result[0]["kind"], "source.fixAll");
    let document = &result[0]["edit"]["documentChanges"][0];
    assert_eq!(document["textDocument"]["version"], 3);
    let inserted: Vec<&serde_json::Value> = document["edits"]
        .as_array()
        .unwrap()
        .iter()
        .map(|edit| &edit["newText"])
        .collect();
    assert_eq!(inserted, [";", ";"], "the two `;`, not `count`");
}

/// A change only waits; a save checks at once, before `notification`
/// returns, and its file is no longer pending.
#[test]
fn a_save_checks_at_once() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-save-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    let file = folder.join("main.peep");
    std::fs::write(&file, "fn main() {}\n").unwrap();
    let uri = Url::from_file_path(&file).unwrap();
    let (connection, client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let change = json!({
        "textDocument": { "uri": uri, "version": 2 },
        "contentChanges": [{ "text": "fn main() { a $ b; }\n" }],
    });
    let change = Notification::new("textDocument/didChange".to_string(), change);
    server.apply_notification(change).unwrap();
    assert!(
        client.receiver.try_recv().is_err(),
        "a change was checked at once"
    );
    assert!(server.deadline.is_some());
    let save = json!({ "textDocument": { "uri": uri } });
    let save = Notification::new("textDocument/didSave".to_string(), save);
    server.apply_notification(save).unwrap();
    let Ok(Message::Notification(published)) = client.receiver.try_recv() else {
        panic!("the save did not publish at once");
    };
    let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
    assert_eq!(
        published.diagnostics.len(),
        1,
        "{:?}",
        published.diagnostics
    );
    assert!(server.pending.is_empty());
}
/// Go to definition and hover over a name answer where it is declared and
/// its type, from the open text; hover over a keyword or an operator,
/// it alone and what it does; elsewhere they answer null.
#[test]
fn answers_definition_and_hover() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-name-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    let file = folder.join("main.peep");
    std::fs::write(&file, "fn main() {}\n").unwrap();
    let uri = Url::from_file_path(&file).unwrap();
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let text = "type Meters = i64;\nfn main() {\n    let total: Meters = 1;\n    let more = total;\n}\n// let me\nstruct Run {\n    length: Meters,\n}\nfn ops(a: i32, x: f64) -> f64 {\n    let q = a/2;\n    const C = -(7 / 2);\n    return x - -1.0;\n}\nfn refs(m: i32) -> i32 {\n    let r = &m;\n    let both = m & 3;\n    let mut x = 1;\n    let p = @x;\n    let s = [2]i32{1, 2};\n    return r.* + s[0];\n}\nconst SMALL: u8 = 200;\nfn literals(n: u8) -> u8 {\n    let wide: f32 = 2;\n    let low: i8 = -128;\n    println(\"hi\", c\"x\");\n    let big: u8 = 300;\n    return n * 3;\n}\nfn walks(r: range[i32]) {}\n#[extern(\"cap\")]\nfn cap() -> u8;\nfn many(values: ...i32) -> &[]i32 in values {\n    return values;\n}\n";
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.apply_notification(open).unwrap();
    let ask = |method: &str, needle: &str| {
        let request = Request::new(
            RequestId::from(1),
            method.to_string(),
            place(&uri, text, needle, 0),
        );
        let Response { result, error, .. } = server.answer_request(request);
        assert!(error.is_none(), "{error:?}");
        result.unwrap()
    };
    let definition = ask("textDocument/definition", "more = to̲tal");
    assert_eq!(definition["uri"], json!(uri));
    assert_eq!(
        definition["range"]["start"],
        json!({ "line": 2, "character": 8 })
    );
    assert_eq!(
        definition["range"]["end"],
        json!({ "line": 2, "character": 13 })
    );
    // Right after a name, before the `;`, is on the name.
    assert_eq!(ask("textDocument/definition", "more = total;̲"), definition);
    let hover = ask("textDocument/hover", "more = to̲tal");
    assert_eq!(
        hover["contents"]["value"],
        json!("```peeper\ntotal: Meters (aka i64)\n```\n\n---\n\n*in* `main` *· line 3*\n")
    );
    assert_eq!(
        ask("textDocument/hover", "\n  ̲  let total"),
        serde_json::Value::Null
    );
    // A `...` parameter is a plain slice inside; its function, as written.
    assert_eq!(
        ask("textDocument/hover", "return v̲alues")["contents"]["value"],
        json!("```peeper\nvalues: &[]i32\n```\n\n---\n\n*in* `main` *· line 34*\n")
    );
    assert_eq!(
        ask("textDocument/hover", "fn m̲any")["contents"]["value"],
        json!(
            "```peeper\nfn many(values: ...i32) -> &[]i32 in values { /* ... */ }\n```\n\n---\n\n*in* `main` *· line 34*\n"
        )
    );
    // A type's name: a declared one answers as its declaration does; a
    // built-in one, what it holds, and is declared nowhere.
    assert_eq!(
        ask("textDocument/hover", "total: M̲eters")["contents"]["value"],
        json!("```peeper\ntype Meters = i64;\n```\n\n---\n\n*in* `main` *· line 1*\n")
    );
    assert_eq!(
        ask("textDocument/definition", "total: M̲eters")["range"]["start"],
        json!({ "line": 0, "character": 5 })
    );
    // In a struct field, outside any body.
    assert_eq!(
        ask("textDocument/hover", "length: M̲eters")["contents"]["value"],
        ask("textDocument/hover", "total: M̲eters")["contents"]["value"]
    );
    assert_eq!(
        ask("textDocument/hover", "type Meters = i̲64")["contents"]["value"],
        json!(
            "```peeper\ni64\n```\n\n---\n\nA signed 64-bit integer: from `-9223372036854775808` to `9223372036854775807`.\n"
        )
    );
    assert_eq!(
        ask("textDocument/definition", "type Meters = i̲64"),
        serde_json::Value::Null
    );
    // A keyword answers what it does; the same word in a comment, nothing.
    let keyword = ask("textDocument/hover", "le̲t total");
    let keyword = keyword["contents"]["value"].as_str().unwrap();
    assert!(keyword.starts_with("```peeper\nlet\n```\n\n---\n\n Declares a variable."));
    assert_eq!(
        ask("textDocument/definition", "le̲t total"),
        serde_json::Value::Null
    );
    assert_eq!(
        ask("textDocument/hover", "// le̲t me"),
        serde_json::Value::Null
    );
    // An operator right after a name is the operator; the name is still
    // the name. `-` subtracts or negates, as the tree says.
    let hover = |needle: &str| ask("textDocument/hover", needle)["contents"]["value"].clone();
    let doc =
        |symbol: &str, doc: &str| json!(format!("```peeper\n{symbol}\n```\n\n---\n\n{doc}\n"));
    assert_eq!(hover("q = a/̲2"), doc("/", BinaryOp::Divide.doc()));
    let name = hover("q = a̲/2");
    assert!(name.as_str().unwrap().starts_with("```peeper\na: i32\n```"));
    assert_eq!(hover("C = -̲(7"), doc("-", UnaryOp::Negate.doc()));
    assert_eq!(hover("(7 /̲ 2)"), doc("/", BinaryOp::Divide.doc()));
    assert_eq!(hover("x -̲ -1.0"), doc("-", BinaryOp::Subtract.doc()));
    assert_eq!(hover("x - -̲1.0"), doc("-", UnaryOp::Negate.doc()));
    assert_eq!(
        ask("textDocument/definition", "(7 /̲ 2)"),
        serde_json::Value::Null
    );
    // A token's own doc covers its uses that are not an operator of the
    // tree: `&` is a reference, but in `m & 3` a bit-and; the `*` of
    // `r.*` reads through `r`, and its `.` reaches into `r`.
    let starts = |needle: &str, text: &str| {
        let found = hover(needle);
        let found = found.as_str().unwrap();
        assert!(found.starts_with(text), "{needle}: {found}");
    };
    starts("r = &̲m", "```peeper\n&\n```\n\n---\n\n Refers to a value");
    assert_eq!(hover("m &̲ 3"), doc("&", BinaryOp::BitAnd.doc()));
    starts("p = @̲x", "```peeper\n@\n```\n\n---\n\n The address");
    starts(
        "s = [̲2]i32",
        "```peeper\n[\n```\n\n---\n\n Indexes an array",
    );
    starts("s[̲0]", "```peeper\n[\n```\n\n---\n\n Indexes an array");
    starts("r.*̲ +", "```peeper\n*\n```\n\n---\n\n Points at a value");
    starts("r.̲* +", "```peeper\n.\n```\n\n---\n\n Reaches into");
    // A literal shows the type its check gave it, the type its place
    // expects or its own, though its value does not fit; an array
    // length, which is not checked as a value, its kind. Neither is declared anywhere, and the place after
    // one is not a name.
    let number = |ty: &str| format!("```peeper\n{ty}\n```\n\n---\n\n A number literal.\n");
    starts("SMALL: u8 = 2̲00", &number("u8"));
    starts("f32 = 2̲", &number("f32"));
    assert_eq!(hover("i8 = -̲128"), doc("-", UnaryOp::Negate.doc()));
    starts("i8 = -12̲8", &number("i8"));
    starts(
        "(\"h̲i\"",
        "```peeper\n&str\n```\n\n---\n\n A string literal",
    );
    starts(
        "c\"̲x\"",
        "```peeper\n&cstr\n```\n\n---\n\n A c string literal",
    );
    starts("u8 = 3̲00", &number("u8"));
    starts("n * 3̲", &number("u8"));
    starts("s = [2̲]i32", &number("number"));
    assert_eq!(
        ask("textDocument/definition", "n * 3̲"),
        serde_json::Value::Null
    );
    assert_eq!(ask("textDocument/hover", "n * 3;̲"), serde_json::Value::Null);
    // A declaration shows its attributes.
    starts(
        "fn c̲ap",
        "```peeper\n#[extern(\"cap\")]\nfn cap() -> u8;\n```",
    );
    // `range` is a built-in type, declared nowhere.
    starts(
        "r: ra̲nge",
        "```peeper\nrange\n```\n\n---\n\n The integers from a start",
    );
    assert_eq!(
        ask("textDocument/definition", "r: ra̲nge"),
        serde_json::Value::Null
    );
}

/// A type's function, called by a dot or by its path, answers as its
/// declaration does; `self`, on the parameter and on a use, shows its
/// binding with the owner's type, then what `self` is; the struct, its
/// functions.
#[test]
fn answers_on_a_types_functions() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-self-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    let file = folder.join("main.peep");
    let text = "struct P { x: i32 }
fn P::Sum(&self) -> i32 {
    return self.x;
}
fn P::Grow(mut self) -> P {
    self.x = self.x + 1;
    return self;
}
fn main() {
    let p = P.{ x = 1 };
    let a = p.Sum();
    let b = P::Sum(&p);
}
fn P::half(&self) -> i32 {
    return self.x / 2;
}
";
    std::fs::write(&file, text).unwrap();
    let uri = Url::from_file_path(&file).unwrap();
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.apply_notification(open).unwrap();
    // Asks `method` on the `nth` `needle` of the text.
    let ask = |method: &str, needle: &str, nth: usize| {
        let request = Request::new(
            RequestId::from(1),
            method.to_string(),
            place(&uri, text, needle, nth),
        );
        let Response { result, error, .. } = server.answer_request(request);
        assert!(error.is_none(), "{error:?}");
        result.unwrap()
    };
    let hover = |needle: &str, nth: usize| {
        let hover = ask("textDocument/hover", needle, nth);
        hover["contents"]["value"].as_str().unwrap().to_string()
    };
    let is_self = |needle: &str, nth: usize, binding: &str| {
        let found = hover(needle, nth);
        let expected = format!("```peeper\n{binding}\n```\n\n---\n\n The value a type's function");
        assert!(found.starts_with(&expected), "{needle} {nth}: {found}");
    };
    is_self("self", 0, "self: &P");
    is_self("self", 1, "self: &P");
    is_self("self", 2, "mut self: P");
    is_self("self", 5, "mut self: P");
    // A struct lists its functions, a private one too in its own module.
    assert_eq!(
        hover("P", 0),
        "```peeper\nstruct P { x: i32 }\n\nfn P::Sum(&self) -> i32 { /* ... */ }\nfn P::Grow(mut self) -> P { /* ... */ }\nfn P::half(&self) -> i32 { /* ... */ }\n```\n\n---\n\n*in* `main` *· line 1*\n"
    );
    let declared = "```peeper\nfn P::Sum(&self) -> i32 { /* ... */ }\n```";
    for nth in [1, 2] {
        assert!(
            hover("Sum", nth).starts_with(declared),
            "{}",
            hover("Sum", nth)
        );
        let definition = ask("textDocument/definition", "Sum", nth);
        assert_eq!(
            definition["range"]["start"],
            json!({ "line": 1, "character": 6 })
        );
    }
}

/// Names across modules: each kind of name answers where it is declared
/// and what it is, in a body with an error too; a constant with its
/// value. After an edit of `main` only, `utils` is reused and `Limit`
/// keeps its value. After an edit that moves `Add` but leaves `utils`'
/// declarations as they were, `main` is reused, and its use of `Add`
/// still finds where `Add` is now. After an edit of only `Twice`'s body,
/// `BIG`, which ran it, has its new value.
#[test]
fn names_across_modules() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-modules-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(folder.join("src")).unwrap();
    std::fs::write(
        folder.join("peeper.toml"),
        "name = \"app\"\nbuild = \"program\"\n",
    )
    .unwrap();
    let utils = "fn Add(a: i32, b: i32) -> i32 { return a + b; }\n#[extern(\"cap\")] fn Cap() -> u8;\n/// The cap.\nconst Limit: u8 = 200;\nfn Twice(n: i32) -> i32 { return n * 2; }\nconst Ratio: f32 = 0.1;\nconst Broken: i32 = true;\nstruct Spot { x: i32 }\nfn Spot::Get(&self) -> i32 { return self.x; }\nfn Spot::raw(&self) -> i32 { return self.x; }\n";
    let main = "import \"app:utils\";
fn main(count: i64) {
    let mut total = count;
    total = 2;
    let sum = utils::Add(1, 2);
    let wide = utils::Limit;
    let wrong: bool = total;
    const DOUBLE = 2 * 21;
    let ratio = utils::Ratio;
    let broken = utils::Broken;
}
const BIG = utils::Twice(21);
struct Holder { place: utils::Spot }
const NAME: &str = \"Ada\";
";
    let (main_file, utils_file) = (folder.join("src/main.peep"), folder.join("src/utils.peep"));
    std::fs::write(&main_file, main).unwrap();
    std::fs::write(&utils_file, utils).unwrap();
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let notify = |server: &mut Server, method: &str, params: serde_json::Value| {
        server
            .apply_notification(Notification::new(method.to_string(), params))
            .unwrap();
    };
    let open = |file: &Path, text: &str| {
        let uri = Url::from_file_path(file).unwrap();
        json!({ "textDocument": { "uri": uri, "languageId": "peeper", "version": 1, "text": text } })
    };
    notify(&mut server, "textDocument/didOpen", open(&main_file, main));
    let main_uri = Url::from_file_path(&main_file).unwrap();
    let ask = |server: &Server, method: &str, needle: &str, nth: usize| {
        let params = place(&main_uri, main, needle, nth);
        let request = Request::new(RequestId::from(1), method.to_string(), params);
        let Response { result, error, .. } = server.answer_request(request);
        assert!(error.is_none(), "{error:?}");
        result.unwrap()
    };
    let hover = |server: &Server, needle: &str, nth: usize| {
        ask(server, "textDocument/hover", needle, nth)["contents"]["value"].clone()
    };
    // Where the name is declared: its file's name and line.
    let declared = |server: &Server, needle: &str, nth: usize| {
        let found = ask(server, "textDocument/definition", needle, nth);
        let uri: Url = serde_json::from_value(found["uri"].clone()).unwrap();
        let file = uri.to_file_path().unwrap();
        let name = file.file_name().unwrap().to_str().unwrap().to_string();
        (name, found["range"]["start"]["line"].clone())
    };
    assert_eq!(
        hover(&server, "count", 1),
        json!("```peeper\ncount: i64\n```\n\n---\n\n*in* `app:main` *· line 2*\n")
    );
    assert_eq!(
        declared(&server, "count", 1),
        ("main.peep".to_string(), json!(1))
    );
    assert_eq!(
        hover(&server, "total", 1),
        json!("```peeper\nmut total: i64\n```\n\n---\n\n*in* `app:main` *· line 3*\n")
    );
    assert_eq!(
        declared(&server, "total", 1),
        ("main.peep".to_string(), json!(2))
    );
    // What is known of a variable is kept once, with its declaration:
    // `count`, `total`, `sum`, `wide`, `wrong`, `ratio`, `broken`.
    let facts = &server.checked[&folder].facts[&main_file];
    let variables = facts
        .iter()
        .filter(|fact| matches!(fact.kind, CachedKind::Variable { .. }));
    assert_eq!(variables.count(), 7);
    assert_eq!(
        hover(&server, "utils", 1),
        json!("```peeper\nimport \"app:utils\"\n```\n")
    );
    assert_eq!(
        hover(&server, "main", 0),
        json!(
            "```peeper\nfn main(count: i64) { /* ... */ }\n```\n\n---\n\n*in* `app:main` *· line 2*\n"
        )
    );
    assert_eq!(
        declared(&server, "main", 0),
        ("main.peep".to_string(), json!(1))
    );
    assert_eq!(
        hover(&server, "Add", 0),
        json!(
            "```peeper\nfn Add(a: i32, b: i32) -> i32 { /* ... */ }\n```\n\n---\n\n*in* `app:utils` *· line 1*\n"
        )
    );
    assert_eq!(
        declared(&server, "Add", 0),
        ("utils.peep".to_string(), json!(0))
    );
    let limit =
        "```peeper\nconst Limit: u8 = 200\n```\n\n---\n\n*in* `app:utils` *· line 4*\n\nThe cap.\n";
    assert_eq!(hover(&server, "Limit", 0), json!(limit));
    // A path in a type answers on both of its names, as one in a value.
    assert_eq!(
        hover(&server, "Spot", 0),
        json!(
            "```peeper\nstruct Spot { x: i32 }\n\nfn Spot::Get(&self) -> i32 { /* ... */ }\n```\n\n---\n\n*in* `app:utils` *· line 8*\n"
        )
    );
    assert_eq!(
        declared(&server, "Spot", 0),
        ("utils.peep".to_string(), json!(7))
    );
    assert_eq!(
        hover(&server, "utils", 6),
        json!("```peeper\nimport \"app:utils\"\n```\n")
    );
    // `main` has an error, and its names are still there.
    assert_eq!(
        hover(&server, "total", 2),
        json!("```peeper\nmut total: i64\n```\n\n---\n\n*in* `app:main` *· line 3*\n")
    );
    assert_eq!(
        hover(&server, "DOUBLE", 0),
        json!(
            "```peeper\nconst DOUBLE: i32 = 42\n\n// from\n2 * 21\n```\n\n---\n\n*in* `app:main` *· line 8*\n"
        )
    );
    let big = |value: &str| {
        format!(
            "```peeper\nconst BIG: i32 = {value}\n\n// from\nutils::Twice(21)\n```\n\n---\n\n*in* `app:main` *· line 12*\n"
        )
    };
    assert_eq!(hover(&server, "BIG", 0), json!(big("42")));
    // A name in a module constant's value, outside any body.
    assert_eq!(
        hover(&server, "Twice", 0),
        json!(
            "```peeper\nfn Twice(n: i32) -> i32 { /* ... */ }\n```\n\n---\n\n*in* `app:utils` *· line 5*\n"
        )
    );
    let i64_hover = "```peeper\ni64\n```\n\n---\n\nA signed 64-bit integer: from `-9223372036854775808` to `9223372036854775807`.\n";
    assert_eq!(hover(&server, "i64", 0), json!(i64_hover));
    assert_eq!(
        hover(&server, "bool", 0),
        json!("```peeper\nbool\n```\n\n---\n\n A truth value: `true` or `false`.\n")
    );
    // An `f32` in its own precision, so its initializer says no more; a
    // constant with an error, as written.
    assert_eq!(
        hover(&server, "Ratio", 0),
        json!("```peeper\nconst Ratio: f32 = 0.1\n```\n\n---\n\n*in* `app:utils` *· line 6*\n")
    );
    // A `&str` as a plain literal, a `&cstr` would show its `c`.
    assert_eq!(
        hover(&server, "NAME", 0),
        json!("```peeper\nconst NAME: &str = \"Ada\"\n```\n\n---\n\n*in* `app:main` *· line 14*\n")
    );
    assert_eq!(
        hover(&server, "Broken", 0),
        json!("```peeper\nconst Broken: i32 = true;\n```\n\n---\n\n*in* `app:utils` *· line 7*\n")
    );

    let edited = format!("{main}// edited\n");
    notify(
        &mut server,
        "textDocument/didOpen",
        open(&main_file, &edited),
    );
    let checked = server.checked.values().next().unwrap();
    let utils_reused = (checked.results.reused()).any(|file| file.path.ends_with("utils.peep"));
    assert!(utils_reused);
    assert_eq!(hover(&server, "Limit", 0), json!(limit));

    let moved = format!("// moved down\n{utils}");
    notify(
        &mut server,
        "textDocument/didOpen",
        open(&utils_file, &moved),
    );
    assert_eq!(
        declared(&server, "Add", 0),
        ("utils.peep".to_string(), json!(1))
    );
    assert_eq!(hover(&server, "i64", 0), json!(i64_hover), "`main` reused");
    // A `const` in a body shifts the arena index of the declarations after
    // it, but `main` is still reused and `Limit` is still `Limit`.
    let with_const = utils.replace("{ return", "{ const K = 1; return");
    notify(
        &mut server,
        "textDocument/didOpen",
        open(&utils_file, &with_const),
    );
    assert_eq!(
        declared(&server, "Limit", 0),
        ("utils.peep".to_string(), json!(3))
    );
    assert!(
        hover(&server, "Limit", 0)
            .as_str()
            .unwrap()
            .contains("const Limit")
    );
    let tripled = with_const.replace("n * 2", "n * 3");
    notify(
        &mut server,
        "textDocument/didOpen",
        open(&utils_file, &tripled),
    );
    assert_eq!(hover(&server, "BIG", 0), json!(big("63")));
}

/// A function with a `const` parameter is built only as its copies, and an
/// editor asks about its text all the same: the names in its body are
/// found in its own module, in every branch, called or not, and stay when
/// that module alone is checked again. A variable of a type its `const T:
/// type` gives has no one type, so it shows its name.
#[test]
fn names_in_a_function_with_const_parameters() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-copies-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(folder.join("src")).unwrap();
    std::fs::write(
        folder.join("peeper.toml"),
        "name = \"app\"\nbuild = \"program\"\n",
    )
    .unwrap();
    let utils = "fn Small() -> i32 { return 1; }
fn Large() -> i32 { return 2; }
const Limit: i32 = 3;
fn Pick(const N: usize, value: i32) -> i32 {
    let kept = value;
    if N == 0 { return Small() + kept; } else { return Large() + Limit; }
}
fn Unused(const T: type, given: T) -> T {
    let mut held = given;
    return Pick(0, Small()) as T;
}
fn Keep<T>(kept: &T, rest: ...i32) -> &T in kept { return kept; }
fn Caller() -> i32 { let one = Pick(0, 1); return Keep(&one, 2).*; }
fn Positive(const N: usize) -> bool { return N > 0; }
fn Sized(const T: type, const N: usize) -> bool { return true; }
fn Fits<Item>(value: Item, const COUNT: usize) -> Item
where
    Item: Sized(8),
    COUNT: Positive
{
    let copied: Item = value;
    return copied;
}
";
    let main = "import \"app:utils\";
fn main() {
    let total = utils::Pick(0, 1) + utils::Pick(1, 1) + utils::Pick(2, 1);
}
";
    let (main_file, utils_file) = (folder.join("src/main.peep"), folder.join("src/utils.peep"));
    std::fs::write(&main_file, main).unwrap();
    std::fs::write(&utils_file, utils).unwrap();
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let notify = |server: &mut Server, method: &str, file: &Path, text: &str| {
        let uri = Url::from_file_path(file).unwrap();
        let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
        let params = match method {
            "textDocument/didOpen" => json!({ "textDocument": document }),
            _ => json!({ "textDocument": document, "contentChanges": [{ "text": text }] }),
        };
        let notification = Notification::new(method.to_string(), params);
        server.apply_notification(notification).unwrap();
        server.check_pending().unwrap();
    };
    let hover = |server: &Server, text: &str, needle: &str, nth: usize| {
        let params = place(
            &Url::from_file_path(&utils_file).unwrap(),
            text,
            needle,
            nth,
        );
        let request = Request::new(RequestId::from(1), "textDocument/hover".to_string(), params);
        let Response { result, error, .. } = server.answer_request(request);
        assert!(error.is_none(), "{error:?}");
        result.unwrap()["contents"]["value"].clone()
    };
    let shown = |facts: &str, line: u32| {
        json!(format!(
            "```peeper\n{facts}\n```\n\n---\n\n*in* `app:utils` *· line {line}*\n"
        ))
    };
    notify(&mut server, "textDocument/didOpen", &main_file, main);
    // Checked again alone, as `main`, which makes the copies, is reused.
    let edited = utils.replace("return 1;", "return 10;");
    let steps = [
        ("textDocument/didOpen", &utils_file, utils, utils),
        (
            "textDocument/didChange",
            &main_file,
            "fn main() {}\n",
            utils,
        ),
        ("textDocument/didChange", &utils_file, &*edited, &*edited),
    ];
    for (method, file, text, utils) in steps {
        notify(&mut server, method, file, text);
        // A name of each branch, and a module constant.
        let small = shown("fn Small() -> i32 { /* ... */ }", 1);
        assert_eq!(hover(&server, utils, "Small", 1), small);
        assert_eq!(
            hover(&server, utils, "Large", 1),
            shown("fn Large() -> i32 { /* ... */ }", 2)
        );
        assert_eq!(
            hover(&server, utils, "Limit", 1),
            shown("const Limit: i32 = 3", 3)
        );
        // Its variables: one of a type every copy has, at a use.
        assert_eq!(hover(&server, utils, "kept", 1), shown("kept: i32", 5));
        // No call copies `Unused`. What it calls is found, a call that
        // would make a copy too; a `T` it is given is no one type.
        assert_eq!(hover(&server, utils, "Small", 2), small);
        assert_eq!(
            hover(&server, utils, "Pick", 1),
            shown(
                "fn Pick(const N: usize, value: i32) -> i32 { /* ... */ }",
                4
            )
        );
        // A call shows the copy it made, then the function as written.
        assert_eq!(
            hover(&server, utils, "Pick", 2),
            shown(
                "fn Pick(const N: usize = 0, value: i32) -> i32\n\n// from\nfn Pick(const N: usize, value: i32) -> i32 { /* ... */ }",
                4
            )
        );
        assert_eq!(
            hover(&server, utils, "Keep", 1),
            shown(
                "fn Keep(kept: &i32, rest: ...i32) -> &i32 in kept\n\n// from\nfn Keep<T>(kept: &T, rest: ...i32) -> &T in kept { /* ... */ }",
                12
            )
        );
        // Its declaration is no call.
        assert_eq!(
            hover(&server, utils, "Keep", 0),
            shown(
                "fn Keep<T>(kept: &T, rest: ...i32) -> &T in kept { /* ... */ }",
                12
            )
        );
        assert_eq!(hover(&server, utils, "given", 1), shown("given", 8));
        assert_eq!(hover(&server, utils, "held", 0), shown("mut held", 9));
        // A type parameter shows where it is declared; only a copy knows its type.
        let item = shown("Item: type", 16);
        assert_eq!(hover(&server, utils, "Item", 3), item); // after `where`
        assert_eq!(hover(&server, utils, "Item", 4), item); // in the body
        assert_eq!(
            hover(&server, utils, "Sized", 1),
            shown(
                "fn Sized(const T: type, const N: usize) -> bool { /* ... */ }",
                15
            )
        );
        assert_eq!(
            hover(&server, utils, "Positive", 1),
            shown("fn Positive(const N: usize) -> bool { /* ... */ }", 14)
        );
        assert_eq!(
            hover(&server, utils, "COUNT", 1),
            shown("const COUNT: usize", 16)
        );
        // Each name once: the copies of `Pick` add none.
        let facts = &server.checked[&folder].facts[&utils_file];
        let declarations = facts
            .iter()
            .filter(|fact| matches!(fact.kind, CachedKind::Declaration { .. }));
        assert_eq!(declarations.count(), 9); // `Caller` calls two, `Fits` asks two
    }
    // What a body with no type for its `T` reports is not shown.
    assert_eq!(server.checked[&folder].results.diagnostics().count(), 0);
}

/// A file the project's entry does not import is checked while it is open.
#[test]
fn checks_an_open_file_nothing_imports() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-unimported-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(folder.join("src")).unwrap();
    std::fs::write(
        folder.join("peeper.toml"),
        "name = \"app\"\nbuild = \"program\"\n",
    )
    .unwrap();
    std::fs::write(folder.join("src/main.peep"), "fn main() {}\n").unwrap();
    let unused = folder.join("src/unused.peep");
    let text = "fn Broken() { let x: bool = 3; }\n";
    std::fs::write(&unused, text).unwrap();
    let uri = Url::from_file_path(&unused).unwrap();
    let (connection, client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.apply_notification(open).unwrap();
    let Message::Notification(published) = client.receiver.recv().unwrap() else {
        panic!("no publish");
    };
    let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
    assert_eq!(published.uri, uri);
    assert_eq!(
        published.diagnostics.len(),
        1,
        "{:?}",
        published.diagnostics
    );
    assert_eq!(
        published.diagnostics[0].code,
        Some(NumberOrString::String("T0001".to_string()))
    );
}

/// A file of a library is checked as part of it: a library has no entry, so
/// its check is kept by its folder, and its other files are checked too.
#[test]
fn checks_a_library_by_its_folder() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-library-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(folder.join("src")).unwrap();
    std::fs::write(
        folder.join("peeper.toml"),
        "name = \"tools\"\nbuild = \"lib\"\n",
    )
    .unwrap();
    let used = folder.join("src/used.peep");
    std::fs::write(&used, "fn Used() -> i32 { return 1; }\n").unwrap();
    let wrong = folder.join("src/wrong.peep");
    std::fs::write(&wrong, "fn Wrong() -> i32 { return true; }\n").unwrap();
    let (connection, client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new("no-libraries"), true);
    let text = std::fs::read_to_string(&used).unwrap();
    let uri = Url::from_file_path(&used).unwrap();
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.apply_notification(open).unwrap();
    let Message::Notification(published) = client.receiver.recv().unwrap() else {
        panic!("no publish");
    };
    let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
    assert_eq!(published.uri, Url::from_file_path(&wrong).unwrap());
    assert_eq!(
        published.diagnostics.len(),
        1,
        "{:?}",
        published.diagnostics
    );
    assert_eq!(server.checked.keys().collect::<Vec<_>>(), [&folder]);
    // Made a program, it is kept by its folder still, and wrong.peep, which
    // it no longer loads, has its diagnostic cleared.
    std::fs::write(folder.join("peeper.toml"), "name = \"tools\"\n").unwrap();
    let main = folder.join("src/main.peep");
    std::fs::write(&main, "fn main() {}\n").unwrap();
    let save = Notification::new(
        "textDocument/didSave".to_string(),
        json!({ "textDocument": { "uri": uri } }),
    );
    server.apply_notification(save).unwrap();
    let cleared = client.receiver.try_iter().any(|message| {
        let Message::Notification(published) = message else {
            return false;
        };
        let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
        published.uri == Url::from_file_path(&wrong).unwrap() && published.diagnostics.is_empty()
    });
    assert!(cleared, "wrong.peep keeps its diagnostic");
    assert_eq!(server.checked.keys().collect::<Vec<_>>(), [&folder]);
}

/// A request answers only from a check of the text the editor has now:
/// not while an edit waits for its check, nor after an edit that could not
/// be loaded. An edit of a bundled library's module checks every program
/// holding it, so each answers with what the module now says.
#[test]
fn answers_only_from_the_current_text() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-current-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    let libraries = folder.join("libs");
    std::fs::create_dir_all(libraries.join("shared/src")).unwrap();
    let shared = "name = \"shared\"\nbuild = \"lib\"\n";
    std::fs::write(libraries.join("shared/peeper.toml"), shared).unwrap();
    let library = libraries.join("shared/src/values.peep");
    std::fs::write(&library, "const Value: i32 = 1;\n").unwrap();
    let uses = "import \"shared:values\";\nfn main() { let v = values::Value; }\n";
    // Two projects, each with a `main` using the library.
    let project = |name: &str| {
        let main = folder.join(name).join("src/main.peep");
        std::fs::create_dir_all(main.parent().unwrap()).unwrap();
        let manifest = format!("name = \"{name}\"\nbuild = \"program\"\n");
        std::fs::write(folder.join(name).join("peeper.toml"), manifest).unwrap();
        std::fs::write(&main, uses).unwrap();
        main
    };
    let (first, second) = (project("first"), project("second"));
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, &libraries, true);
    let notify = |server: &mut Server, method: &str, file: &Path, text: &str| {
        let uri = Url::from_file_path(file).unwrap();
        let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
        let params = match method {
            "textDocument/didOpen" => json!({ "textDocument": document }),
            _ => json!({ "textDocument": document, "contentChanges": [{ "text": text }] }),
        };
        let notification = Notification::new(method.to_string(), params);
        server.apply_notification(notification).unwrap();
    };
    // Hover on `Value` in `file`.
    let hover = |server: &Server, file: &Path| {
        let place = json!({
            "textDocument": { "uri": Url::from_file_path(file).unwrap() },
            "position": { "line": 1, "character": 30 },
        });
        let request = Request::new(RequestId::from(1), "textDocument/hover".to_string(), place);
        let Response { result, .. } = server.answer_request(request);
        result.unwrap()["contents"]["value"].clone()
    };
    notify(&mut server, "textDocument/didOpen", &first, uses);
    notify(&mut server, "textDocument/didOpen", &second, uses);
    let value = |value: &str| {
        let hover = format!(
            "```peeper\nconst Value: i32 = {value}\n```\n\n---\n\n*in* `shared:values` *· line 1*\n"
        );
        json!(hover)
    };
    assert_eq!(hover(&server, &second), value("1"));
    notify(
        &mut server,
        "textDocument/didOpen",
        &library,
        "const Value: i32 = 2;\n",
    );
    assert_eq!(hover(&server, &first), value("2"));
    assert_eq!(hover(&server, &second), value("2"), "both programs checked");
    let moved = format!("\n{uses}");
    notify(&mut server, "textDocument/didChange", &second, &moved);
    assert_eq!(
        hover(&server, &second),
        serde_json::Value::Null,
        "not checked yet"
    );
    server.check_pending().unwrap();
    let broken = format!("{uses}fn (\n");
    notify(&mut server, "textDocument/didOpen", &second, &broken);
    assert_eq!(
        hover(&server, &second),
        serde_json::Value::Null,
        "not loaded"
    );
}
