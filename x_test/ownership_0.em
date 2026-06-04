type Reader fn(&i32, &mut i32, *const i32, *mut i32): &i32;

type SharedRef &i32;
type UniqueRef &mut i32;
type RawIn *const i32;
type RawOut *mut i32;

fn takes_shared(x: &i32) -> i32 {
    return 0;
}

fn takes_mut(x: &mut i32) -> i32 {
    return 0;
}

fn main() -> i32 {
    let mut value: i32 = 0;

    let a = &value;
    let b = &mut value;

    return 0;
}
