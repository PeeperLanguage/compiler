source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [1 x i8] c" "
@.str.1 = private unnamed_addr constant [1 x i8] c"\0A"

define void @"ll_const_parameters:main.main"() {
entry:
  %s0 = alloca [8 x i8], align 8
  %s1 = alloca [8 x i8], align 8
  %s2 = alloca [8 x i8], align 8
  %s3 = alloca [8 x i8], align 8
  %s4 = alloca [8 x i8], align 8
  %s5 = alloca [8 x i8], align 8
  %s6 = alloca [8 x i8], align 8
  %s7 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  %i0 = call i64 @"ll_const_parameters:main.Size<i32>"()
  %i1 = call i64 @"ll_const_parameters:main.Size<i64>"()
  %i2 = call i64 @"ll_const_parameters:main.Size<i32>"()
  %i3 = call i64 @"ll_const_parameters:main.Size<i64>"()
  store i64 %i0, ptr %s0
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s0)
  store i64 %i1, ptr %s1
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s1)
  store i64 %i2, ptr %s2
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s2)
  store i64 %i3, ptr %s3
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s3)
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 1)
  call void @"peeper_rt_v1_flush"()
  %i25 = call i64 @"ll_const_parameters:main.Twice<2>"()
  %i26 = call i64 @"ll_const_parameters:main.Twice<3>"()
  %i27 = call i64 @"ll_const_parameters:main.Twice<2>"()
  %i28 = call i64 @"ll_const_parameters:main.Twice<3>"()
  store i64 %i25, ptr %s4
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s4)
  store i64 %i26, ptr %s5
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s5)
  store i64 %i27, ptr %s6
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s6)
  store i64 %i28, ptr %s7
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s7)
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 1)
  call void @"peeper_rt_v1_flush"()
  ret void
}

define i64 @"ll_const_parameters:main.Twice<3>"() {
entry:
  br label %bb0
bb0:
  ret i64 6
}

define i64 @"ll_const_parameters:main.Twice<2>"() {
entry:
  br label %bb0
bb0:
  ret i64 4
}

define i64 @"ll_const_parameters:main.Size<i64>"() {
entry:
  br label %bb0
bb0:
  ret i64 8
}

define i64 @"ll_const_parameters:main.Size<i32>"() {
entry:
  br label %bb0
bb0:
  ret i64 4
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

