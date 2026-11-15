source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [2 x i8] c"\0A\00"

define i64 @"ll_size_of:main.Bytes"(i64 %a0) {
entry:
  %s0 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store i64 %a0, ptr %s0
  %i1 = load i64, ptr %s0
  %i2 = mul i64 %i1, 16
  %i3 = add i64 %i2, 8
  ret i64 %i3
}

define void @"ll_size_of:main.main"() {
entry:
  %s0 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  %i0 = call i64 @"ll_size_of:main.Bytes"(i64 3)
  store i64 %i0, ptr %s0
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s0)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v1_flush"()
  ret void
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

