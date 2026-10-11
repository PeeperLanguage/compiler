package thir_test

import (
	"strings"
	"testing"
)

func TestModuleTextShowsQualifiedTypesAndStructure(t *testing.T) {
	mod := buildTypedModule(t, `
struct Point { x: i32, y: i32 }
enum Result { Ok: struct { value: i32 }, Pending }
fn read(result: Result) -> i32 {
    match result {
        Result::Ok => { return result.value; }
        Result::Pending => { return 0; }
    }
}
fn main() -> i32 {
    let mut total: i32 = 0;
    let point: Point = Point.{ x = 1, y = 2 };
    for index, value in 0..2 { total = total + index + value; }
    if total > 1 { total = total - 1; } else { total = point.x; }
    let wide: i64 = total as i64;
    return read(Result::Ok with .{ value = total });
}`)

	text := mod.THIR.Text()
	for _, want := range []string{
		"; thir module thir_test\n",
		"fn read(result: local:thir_test::Result) -> i32 {\n",
		"  match result {\n",
		"    Ok => {\n",
		"      return result.value\n",
		"    Pending => {\n",
		"  let point: local:thir_test::Point = local:thir_test::Point{x: 1, y: 2}\n",
		"  for index, value in 0..2 {\n",
		"    total = ((total + index) + value)\n",
		"  if (total > 1) {\n",
		"  } else {\n",
		"  let wide: i64 = (total as i64)\n",
		"  return read(local:thir_test::Result.Ok(",
		"): i32\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("THIR text missing %q; got:\n%s", want, text)
		}
	}
	if again := mod.THIR.Text(); again != text {
		t.Fatalf("THIR text changed between calls:\n%s\n---\n%s", text, again)
	}
}
