fn main() -> i32 {
    // Test all integer types with casts
    let i8_val: i8 = 127;
    let i16_val: i16 = 32767;
    let i32_val: i32 = 2147483647;
    let i64_val: i64 = 9223372036854775807;
    
    let u8_val: u8 = 255;
    let u16_val: u16 = 65535;
    let u32_val: u32 = 4294967295;
    let u64_val: u64 = 18446744073709551615;
    
    let f32_val: f32 = 3.14;
    let f64_val: f64 = 3.141592653589793;
    
    // Casts between all types
    let a: i32 = i8_val as i32;
    let b: i32 = i16_val as i32;
    let c: i64 = i32_val as i64;
    let d: i32 = u8_val as i32;
    let e: i32 = u16_val as i32;
    let f: i64 = u32_val as i64;
    let g: f32 = i32_val as f32;
    let h: f64 = i32_val as f64;
    let i: i32 = f32_val as i32;
    let j: i32 = f64_val as i32;
    let k: f64 = f32_val as f64;
    
    // Binary expressions with casts
    let sum1: i32 = (100 as i32) + (200 as i32);
    let sum2: f64 = (1.5 as f64) + (2.5 as f64);
    let mixed: f64 = (10 as f64) + (2.5 as f64);
    
    // Unary expressions with casts
    let neg: i32 = -(5 as i32);
    let pos: i32 = +(10 as i32);
    
    // Very large numbers
    let large_i64: i64 = 9223372036854775807 as i64;
    let large_u64: u64 = 18446744073709551615 as u64;
    let large_f64: f64 = 1.7976931348623157e+308 as f64;
    
    // Very small numbers
    let small_i8: i8 = -128 as i8;
    let small_f64: f64 = 2.2250738585072014e-308 as f64;
    
    // Zero
    let zero_i32: i32 = 0 as i32;
    let zero_f64: f64 = 0.0 as f64;
    
    return 0;
}