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
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use serde_json::json;
use source::SourceFile;
use source::Span;

use crate::DEBOUNCE;
use crate::OpenFile;
use crate::checked::CachedKind;
use crate::convert::lsp_diagnostics;
use crate::convert::offset;
use crate::convert::position;
use crate::serve_connection;
use crate::server::Server;

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
    let server = Server::new(&connection, Path::new(""), true);
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
    let unchecked = Server::new(&connection, Path::new(""), false);
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
    let thread = std::thread::spawn(move || serve_connection(server, Path::new("")).unwrap());
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
    let mut server = Server::new(&connection, Path::new(""), true);
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 3, "text": text });
    let open = json!({ "textDocument": document });
    let open = Notification::new("textDocument/didOpen".to_string(), open);
    server.handle_notification(open).unwrap();
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
    let mut server = Server::new(&connection, Path::new(""), true);
    let change = json!({
        "textDocument": { "uri": uri, "version": 2 },
        "contentChanges": [{ "text": "fn main() { a $ b; }\n" }],
    });
    let change = Notification::new("textDocument/didChange".to_string(), change);
    server.handle_notification(change).unwrap();
    assert!(
        client.receiver.try_recv().is_err(),
        "a change was checked at once"
    );
    assert!(server.deadline.is_some());
    let save = json!({ "textDocument": { "uri": uri } });
    let save = Notification::new("textDocument/didSave".to_string(), save);
    server.handle_notification(save).unwrap();
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
    let mut server = Server::new(&connection, Path::new(""), true);
    let text = "type Meters = i64;\nfn main() {\n    let total: Meters = 1;\n    let more = total;\n}\n// let me\nstruct Run {\n    length: Meters,\n}\nfn ops(a: i32, x: f64) -> f64 {\n    let q = a/2;\n    const C = -(7 / 2);\n    return x - -1.0;\n}\nfn refs(m: i32) -> i32 {\n    let r = &m;\n    let both = m & 3;\n    let mut x = 1;\n    let p = @x;\n    let s = [2]i32{1, 2};\n    return r.* + s[0];\n}\nconst SMALL: u8 = 200;\nfn literals(n: u8) -> u8 {\n    let wide: f32 = 2;\n    let low: i8 = -128;\n    println(\"hi\", c\"x\");\n    let big: u8 = 300;\n    return n * 3;\n}\nfn walks(r: range[i32]) {}\n#[extern(\"cap\")]\nfn cap() -> u8;\n";
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.handle_notification(open).unwrap();
    let ask = |method: &str, line: u32, character: u32| {
        let place = json!({
            "textDocument": { "uri": uri },
            "position": { "line": line, "character": character },
        });
        let request = Request::new(RequestId::from(1), method.to_string(), place);
        let Response { result, error, .. } = server.answer_request(request);
        assert!(error.is_none(), "{error:?}");
        result.unwrap()
    };
    let definition = ask("textDocument/definition", 3, 16);
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
    assert_eq!(ask("textDocument/definition", 3, 20), definition);
    let hover = ask("textDocument/hover", 3, 16);
    assert_eq!(
        hover["contents"]["value"],
        json!("```peeper\ntotal: Meters (aka i64)\n```\n\n---\n\n*in* `main` *· line 3*\n")
    );
    assert_eq!(ask("textDocument/hover", 2, 1), serde_json::Value::Null);
    // A type's name: a declared one answers as its declaration does; a
    // built-in one, what it holds, and is declared nowhere.
    assert_eq!(
        ask("textDocument/hover", 2, 15)["contents"]["value"],
        json!("```peeper\ntype Meters = i64;\n```\n\n---\n\n*in* `main` *· line 1*\n")
    );
    assert_eq!(
        ask("textDocument/definition", 2, 15)["range"]["start"],
        json!({ "line": 0, "character": 5 })
    );
    // In a struct field, outside any body.
    assert_eq!(
        ask("textDocument/hover", 7, 12)["contents"]["value"],
        ask("textDocument/hover", 2, 15)["contents"]["value"]
    );
    assert_eq!(
        ask("textDocument/hover", 0, 14)["contents"]["value"],
        json!(
            "```peeper\ni64\n```\n\n---\n\nA signed 64-bit integer: from `-9223372036854775808` to `9223372036854775807`.\n"
        )
    );
    assert_eq!(
        ask("textDocument/definition", 0, 14),
        serde_json::Value::Null
    );
    // A keyword answers what it does; the same word in a comment, nothing.
    let keyword = ask("textDocument/hover", 2, 5);
    let keyword = keyword["contents"]["value"].as_str().unwrap();
    assert!(keyword.starts_with("```peeper\nlet\n```\n\n---\n\n Declares a variable."));
    assert_eq!(
        ask("textDocument/definition", 2, 5),
        serde_json::Value::Null
    );
    assert_eq!(ask("textDocument/hover", 5, 4), serde_json::Value::Null);
    // An operator right after a name is the operator; the name is still
    // the name. `-` subtracts or negates, as the tree says.
    let hover = |line: u32, character: u32| {
        ask("textDocument/hover", line, character)["contents"]["value"].clone()
    };
    let doc =
        |symbol: &str, doc: &str| json!(format!("```peeper\n{symbol}\n```\n\n---\n\n{doc}\n"));
    assert_eq!(hover(10, 13), doc("/", BinaryOp::Divide.doc()));
    let name = hover(10, 12);
    assert!(name.as_str().unwrap().starts_with("```peeper\na: i32\n```"));
    assert_eq!(hover(11, 14), doc("-", UnaryOp::Negate.doc()));
    assert_eq!(hover(11, 18), doc("/", BinaryOp::Divide.doc()));
    assert_eq!(hover(12, 13), doc("-", BinaryOp::Subtract.doc()));
    assert_eq!(hover(12, 15), doc("-", UnaryOp::Negate.doc()));
    assert_eq!(
        ask("textDocument/definition", 11, 18),
        serde_json::Value::Null
    );
    // A token's own doc covers its uses that are not an operator of the
    // tree: `&` is a reference, but in `m & 3` a bit-and; the `*` of
    // `r.*` reads through `r`, and its `.` reaches into `r`.
    let starts = |line: u32, character: u32, text: &str| {
        let found = hover(line, character);
        let found = found.as_str().unwrap();
        assert!(found.starts_with(text), "{line}:{character}: {found}");
    };
    starts(15, 12, "```peeper\n&\n```\n\n---\n\n Refers to a value");
    assert_eq!(hover(16, 17), doc("&", BinaryOp::BitAnd.doc()));
    starts(18, 12, "```peeper\n@\n```\n\n---\n\n The address");
    starts(19, 12, "```peeper\n[\n```\n\n---\n\n Indexes an array");
    starts(20, 18, "```peeper\n[\n```\n\n---\n\n Indexes an array");
    starts(20, 13, "```peeper\n*\n```\n\n---\n\n Points at a value");
    starts(20, 12, "```peeper\n.\n```\n\n---\n\n Reaches into");
    // A literal shows the type its check gave it, the type its place
    // expects or its own, though its value does not fit; an array
    // length, which is not checked as a value, its kind. Neither is declared anywhere, and the place after
    // one is not a name.
    let number = |ty: &str| format!("```peeper\n{ty}\n```\n\n---\n\n A number literal.\n");
    starts(22, 18, &number("u8"));
    starts(24, 20, &number("f32"));
    assert_eq!(hover(25, 18), doc("-", UnaryOp::Negate.doc()));
    starts(25, 20, &number("i8"));
    starts(26, 13, "```peeper\ncstr\n```\n\n---\n\n A string literal");
    starts(26, 19, "```peeper\ncstr\n```\n\n---\n\n A c string literal");
    starts(27, 18, &number("u8"));
    starts(28, 15, &number("u8"));
    starts(19, 13, &number("number"));
    assert_eq!(
        ask("textDocument/definition", 28, 15),
        serde_json::Value::Null
    );
    assert_eq!(ask("textDocument/hover", 28, 16), serde_json::Value::Null);
    // A declaration shows its attributes.
    starts(32, 3, "```peeper\n#[extern(\"cap\")]\nfn cap() -> u8");
    // `range` is a built-in type, declared nowhere.
    starts(
        30,
        13,
        "```peeper\nrange\n```\n\n---\n\n The integers from a start",
    );
    assert_eq!(
        ask("textDocument/definition", 30, 13),
        serde_json::Value::Null
    );
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
        "name = \"app\"\nbuild = \"bin\"\n",
    )
    .unwrap();
    let utils = "fn Add(a: i32, b: i32) -> i32 { return a + b; }\n#[extern(\"cap\")] fn Cap() -> u8;\n/// The cap.\nconst Limit: u8 = 200;\nfn Twice(n: i32) -> i32 { return n * 2; }\nconst Ratio: f32 = 0.1;\nconst Broken: i32 = true;\n";
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
";
    let (main_file, utils_file) = (folder.join("src/main.peep"), folder.join("src/utils.peep"));
    std::fs::write(&main_file, main).unwrap();
    std::fs::write(&utils_file, utils).unwrap();
    let (connection, _client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new(""), true);
    let notify = |server: &mut Server, method: &str, params: serde_json::Value| {
        server
            .handle_notification(Notification::new(method.to_string(), params))
            .unwrap();
    };
    let open = |file: &Path, text: &str| {
        let uri = Url::from_file_path(file).unwrap();
        json!({ "textDocument": { "uri": uri, "languageId": "peeper", "version": 1, "text": text } })
    };
    notify(&mut server, "textDocument/didOpen", open(&main_file, main));
    // The place of the `nth` `needle` in `main`.
    let place = |needle: &str, nth: usize| {
        let at = main.match_indices(needle).nth(nth).unwrap().0;
        let position = position(&SourceFile::new("main.peep", main), at);
        json!({
            "textDocument": { "uri": Url::from_file_path(&main_file).unwrap() },
            "position": position,
        })
    };
    let ask = |server: &Server, method: &str, needle: &str, nth: usize| {
        let request = Request::new(RequestId::from(1), method.to_string(), place(needle, nth));
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
    let facts = &server.checked[&main_file].facts[&main_file];
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
        json!("```peeper\nfn main(count: i64)\n```\n\n---\n\n*in* `app:main` *· line 2*\n")
    );
    assert_eq!(
        declared(&server, "main", 0),
        ("main.peep".to_string(), json!(1))
    );
    assert_eq!(
        hover(&server, "Add", 0),
        json!(
            "```peeper\nfn Add(a: i32, b: i32) -> i32\n```\n\n---\n\n*in* `app:utils` *· line 1*\n"
        )
    );
    assert_eq!(
        declared(&server, "Add", 0),
        ("utils.peep".to_string(), json!(0))
    );
    let limit =
        "```peeper\nconst Limit: u8 = 200\n```\n\n---\n\n*in* `app:utils` *· line 4*\n\nThe cap.\n";
    assert_eq!(hover(&server, "Limit", 0), json!(limit));
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
        json!("```peeper\nfn Twice(n: i32) -> i32\n```\n\n---\n\n*in* `app:utils` *· line 5*\n")
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

/// A file the project's entry does not import is checked while it is open.
#[test]
fn checks_an_open_file_nothing_imports() {
    let folder = std::env::temp_dir().join(format!("peeper-lsp-unimported-{}", std::process::id()));
    _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(folder.join("src")).unwrap();
    std::fs::write(
        folder.join("peeper.toml"),
        "name = \"app\"\nbuild = \"bin\"\n",
    )
    .unwrap();
    std::fs::write(folder.join("src/main.peep"), "fn main() {}\n").unwrap();
    let unused = folder.join("src/unused.peep");
    let text = "fn Broken() { let x: bool = 3; }\n";
    std::fs::write(&unused, text).unwrap();
    let uri = Url::from_file_path(&unused).unwrap();
    let (connection, client) = Connection::memory();
    let mut server = Server::new(&connection, Path::new(""), true);
    let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
    let open = Notification::new(
        "textDocument/didOpen".to_string(),
        json!({ "textDocument": document }),
    );
    server.handle_notification(open).unwrap();
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
    let library = libraries.join("shared/src/values.peep");
    std::fs::write(&library, "const Value: i32 = 1;\n").unwrap();
    let uses = "import \"shared:values\";\nfn main() { let v = values::Value; }\n";
    // Two projects, each with a `main` using the library.
    let project = |name: &str| {
        let main = folder.join(name).join("src/main.peep");
        std::fs::create_dir_all(main.parent().unwrap()).unwrap();
        let manifest = format!("name = \"{name}\"\nbuild = \"bin\"\n");
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
        server.handle_notification(notification).unwrap();
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
