//! Times this demo's parser against the real compiler's on one generated
//! text both accept: `cargo run --release --example parse_speed`.

use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;

use gateway::syntax::Parser;

fn text(functions: usize) -> String {
    let mut text = String::new();
    for index in 0..functions {
        text.push_str(&format!(
            "struct Point{index} {{ x: i32, y: i32 }}
fn f{index}(a: i32, b: &mut Point{index}) -> i32 {{
    let mut sum = a * 3 + 1;
    let mut i = 0;
    for i < 10 {{ sum = sum + i; i = i + 1; }}
    if sum < 100 {{ sum = sum + b.x; }} else {{ sum = sum - 1; }}
    b.y = sum;
    let p = Point{index}.{{ x = 1, y = 2 }};
    return sum + p.x + f{index}(a, b);
}}
"
        ));
    }
    text
}

fn best(mut run: impl FnMut()) -> Duration {
    (0..7)
        .map(|_| {
            let start = Instant::now();
            run();
            start.elapsed()
        })
        .min()
        .expect("seven runs")
}

fn main() {
    let text = text(20_000);
    let demo = best(|| {
        let mut parser = Parser::new(&text, &language::RULES).expect("lexes");
        let declarations = parser.program().expect("parses");
        assert_eq!(declarations.len(), 40_000);
    });
    let file = Arc::new(source::SourceFile::new("speed.peep", text.clone()));
    let real = best(|| {
        let (_, errors) = parser::parse(&file);
        assert!(errors.is_empty(), "the real parser refused the text");
    });
    let megabytes = text.len() as f64 / 1e6;
    println!("text: {megabytes:.1} MB");
    println!("demo parser: {demo:?}");
    println!("real parser: {real:?}");
}
