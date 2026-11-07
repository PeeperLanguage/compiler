source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [2 x i8] c" \00"
@.str.1 = private unnamed_addr constant [2 x i8] c"\0A\00"

define void @"ll_const_parameters:main.main"() {
entry:
  %i4.0.m = alloca i64
  %i4.1.m = alloca i64
  %i4.2.m = alloca i64
  %i4.3.m = alloca i64
  %i9.0.m = alloca i64
  %i9.1.m = alloca i64
  %i9.2.m = alloca i64
  %i9.3.m = alloca i64
  br label %bb0
bb0:
  %i0 = call i64 @"ll_const_parameters:main.Size<i32>"()
  %i1 = call i64 @"ll_const_parameters:main.Size<i64>"()
  %i2 = call i64 @"ll_const_parameters:main.Size<i32>"()
  %i3 = call i64 @"ll_const_parameters:main.Size<i64>"()
  store i64 %i0, ptr %i4.0.m
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i4.0.m)
  store i64 %i1, ptr %i4.1.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i4.1.m)
  store i64 %i2, ptr %i4.2.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i4.2.m)
  store i64 %i3, ptr %i4.3.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i4.3.m)
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 1)
  call void @"peeper_rt_v1_flush"()
  %i5 = call i64 @"ll_const_parameters:main.Twice<2>"()
  %i6 = call i64 @"ll_const_parameters:main.Twice<3>"()
  %i7 = call i64 @"ll_const_parameters:main.Twice<2>"()
  %i8 = call i64 @"ll_const_parameters:main.Twice<3>"()
  store i64 %i5, ptr %i9.0.m
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i9.0.m)
  store i64 %i6, ptr %i9.1.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i9.1.m)
  store i64 %i7, ptr %i9.2.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i9.2.m)
  store i64 %i8, ptr %i9.3.m
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v2_print_int"(i32 0, i32 1, ptr %i9.3.m)
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

