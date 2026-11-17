source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [2 x i8] c" \00"
@.str.1 = private unnamed_addr constant [2 x i8] c"\0A\00"

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
  store i64 %i0, ptr %s0
  %i2 = call i64 @"ll_const_parameters:main.Size<i64>"()
  store i64 %i2, ptr %s1
  %i4 = call i64 @"ll_const_parameters:main.Size<i32>"()
  store i64 %i4, ptr %s2
  %i6 = call i64 @"ll_const_parameters:main.Size<i64>"()
  store i64 %i6, ptr %s3
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s0)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s1)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s2)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s3)
  call void @"peeper_rt_v1_print_text"(ptr @.str.1)
  call void @"peeper_rt_v1_flush"()
  %i25 = call i64 @"ll_const_parameters:main.Twice<2>"()
  store i64 %i25, ptr %s4
  %i27 = call i64 @"ll_const_parameters:main.Twice<3>"()
  store i64 %i27, ptr %s5
  %i29 = call i64 @"ll_const_parameters:main.Twice<2>"()
  store i64 %i29, ptr %s6
  %i31 = call i64 @"ll_const_parameters:main.Twice<3>"()
  store i64 %i31, ptr %s7
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s4)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s5)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s6)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %s7)
  call void @"peeper_rt_v1_print_text"(ptr @.str.1)
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

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

