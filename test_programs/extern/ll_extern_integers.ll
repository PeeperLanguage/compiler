source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [32 x i8] c"\FE\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\FF\BF\FF\FF\FF\FF\FF\FF\00\00\00\00\00\00\00"
@.str.1 = private unnamed_addr constant [32 x i8] c"\0B\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00@\00\00\00\00\00\00\00\00"
@.str.2 = private unnamed_addr constant [32 x i8] c"\0D\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00\80\00\00\00\00\00\00\00"

define void @"ll_extern_integers:main.main"() {
entry:
  %i18.r = alloca [16 x i8], align 16
  %i22.r = alloca [16 x i8], align 16
  %i31.r = alloca [32 x i8], align 8
  %i31.0 = alloca [32 x i8], align 8
  %i35.3 = alloca [16 x i8], align 8
  %i35.4 = alloca [16 x i8], align 8
  %i35.r = alloca [16 x i8], align 16
  %i40.r = alloca [32 x i8], align 8
  %i40.3 = alloca [16 x i8], align 8
  %i40.6 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  %i0 = call zeroext i8 @"c_b"(i8 zeroext 1)
  %bb0.c = icmp ne i8 %i0, 0
  br i1 %bb0.c, label %bb1, label %bb2
bb1:
  call void @"peeper_rt_v1_exit"(i32 20)
  br label %bb2
bb2:
  %i2 = call signext i8 @"c_s8"(i8 signext -3)
  %i3.c = icmp ne i8 %i2, -4
  br i1 %i3.c, label %bb3, label %bb4
bb3:
  call void @"peeper_rt_v1_exit"(i32 21)
  br label %bb4
bb4:
  %i5 = call zeroext i16 @"c_u16"(i16 zeroext -536)
  %i6.c = icmp ne i16 %i5, -535
  br i1 %i6.c, label %bb5, label %bb6
bb5:
  call void @"peeper_rt_v1_exit"(i32 22)
  br label %bb6
bb6:
  %i8 = call signext i8 @"c_s5"(i8 signext -7)
  %i9 = shl i8 %i8, 3
  %i10 = ashr i8 %i9, 3
  %i11.c = icmp ne i8 %i10, -8
  br i1 %i11.c, label %bb7, label %bb8
bb7:
  call void @"peeper_rt_v1_exit"(i32 23)
  br label %bb8
bb8:
  %i13 = call i64 @"c_s33"(i64 -2147483643)
  %i14 = shl i64 %i13, 31
  %i15 = ashr i64 %i14, 31
  %i16.c = icmp ne i64 %i15, 2147483642
  br i1 %i16.c, label %bb9, label %bb10
bb9:
  call void @"peeper_rt_v1_exit"(i32 24)
  br label %bb10
bb10:
  %i18.0.s0 = lshr i128 18446744073709551619, 0
  %i18.0.p0 = trunc i128 %i18.0.s0 to i64
  %i18.0.s1 = lshr i128 18446744073709551619, 64
  %i18.0.p1 = trunc i128 %i18.0.s1 to i64
  %i18.c = call { i64, i64 } @"c_u65"(i64 %i18.0.p0, i64 %i18.0.p1)
  store { i64, i64 } %i18.c, ptr %i18.r, align 16
  %i18 = load i128, ptr %i18.r, align 16
  %i19 = and i128 %i18, 36893488147419103231
  %i20.c = icmp ne i128 %i19, 18446744073709551620
  br i1 %i20.c, label %bb11, label %bb12
bb11:
  call void @"peeper_rt_v1_exit"(i32 25)
  br label %bb12
bb12:
  %i22.0.s0 = lshr i128 -1180591620717411303433, 0
  %i22.0.p0 = trunc i128 %i22.0.s0 to i64
  %i22.0.s1 = lshr i128 -1180591620717411303433, 64
  %i22.0.p1 = trunc i128 %i22.0.s1 to i64
  %i22.c = call { i64, i64 } @"c_s100"(i64 %i22.0.p0, i64 %i22.0.p1)
  store { i64, i64 } %i22.c, ptr %i22.r, align 16
  %i22 = load i128, ptr %i22.r, align 16
  %i23 = shl i128 %i22, 28
  %i24 = ashr i128 %i23, 28
  %i25.c = icmp ne i128 %i24, 1237940039285380274899124225
  br i1 %i25.c, label %bb13, label %bb14
bb13:
  call void @"peeper_rt_v1_exit"(i32 26)
  br label %bb14
bb14:
  %i27 = call i128 @"c_s128"(i128 -1267650600228229401496703205377)
  %i28.c = icmp ne i128 %i27, 1329227995784915872903807060280344583
  br i1 %i28.c, label %bb15, label %bb16
bb15:
  call void @"peeper_rt_v1_exit"(i32 27)
  br label %bb16
bb16:
  %i30 = load i200, ptr @.str.0, align 1
  %i31.0.x = sext i200 %i30 to i256
  store i256 %i31.0.x, ptr %i31.0, align 8
  %i31.1.s0 = lshr i128 -9223372036854775809, 0
  %i31.1.p0 = trunc i128 %i31.1.s0 to i64
  %i31.1.s1 = lshr i128 -9223372036854775809, 64
  %i31.1.p1 = trunc i128 %i31.1.s1 to i64
  call void @"c_s200"(ptr sret([32 x i8]) align 8 %i31.r, ptr byval([32 x i8]) align 8 %i31.0, i64 %i31.1.p0, i64 %i31.1.p1)
  %i31.w = load i256, ptr %i31.r, align 8
  %i31 = trunc i256 %i31.w to i200
  %i32 = load i200, ptr @.str.1, align 1
  %i33.c = icmp ne i200 %i31, %i32
  br i1 %i33.c, label %bb17, label %bb18
bb17:
  call void @"peeper_rt_v1_exit"(i32 28)
  br label %bb18
bb18:
  %i35.0.s0 = lshr i128 1, 0
  %i35.0.p0 = trunc i128 %i35.0.s0 to i64
  %i35.0.s1 = lshr i128 1, 64
  %i35.0.p1 = trunc i128 %i35.0.s1 to i64
  %i35.1.s0 = lshr i128 -2, 0
  %i35.1.p0 = trunc i128 %i35.1.s0 to i64
  %i35.1.s1 = lshr i128 -2, 64
  %i35.1.p1 = trunc i128 %i35.1.s1 to i64
  store i128 18446744073709551615, ptr %i35.3, align 8
  store i128 -5, ptr %i35.4, align 8
  %i35.c = call { i64, i64 } @"c_many"(i64 %i35.0.p0, i64 %i35.0.p1, i64 %i35.1.p0, i64 %i35.1.p1, i32 3, ptr byval([16 x i8]) align 8 %i35.3, ptr byval([16 x i8]) align 8 %i35.4, i64 6)
  store { i64, i64 } %i35.c, ptr %i35.r, align 16
  %i35 = load i128, ptr %i35.r, align 16
  %i36 = shl i128 %i35, 63
  %i37 = ashr i128 %i36, 63
  %i38.c = icmp ne i128 %i37, -9223372036854775815
  br i1 %i38.c, label %bb19, label %bb20
bb19:
  call void @"peeper_rt_v1_exit"(i32 29)
  br label %bb20
bb20:
  %i40.0.s0 = lshr i128 -1, 0
  %i40.0.p0 = trunc i128 %i40.0.s0 to i64
  %i40.0.s1 = lshr i128 -1, 64
  %i40.0.p1 = trunc i128 %i40.0.s1 to i64
  %i40.2.s0 = lshr i128 2, 0
  %i40.2.p0 = trunc i128 %i40.2.s0 to i64
  %i40.2.s1 = lshr i128 2, 64
  %i40.2.p1 = trunc i128 %i40.2.s1 to i64
  store i128 -18446744073709551616, ptr %i40.3, align 8
  store i128 18446744073709551617, ptr %i40.6, align 8
  call void @"c_mix"(ptr sret([32 x i8]) align 8 %i40.r, i64 %i40.0.p0, i64 %i40.0.p1, double 0x3FF8000000000000, i64 %i40.2.p0, i64 %i40.2.p1, ptr byval([16 x i8]) align 8 %i40.3, double 0x4004000000000000, i32 -9, ptr byval([16 x i8]) align 8 %i40.6)
  %i40.w = load i256, ptr %i40.r, align 8
  %i40 = trunc i256 %i40.w to i200
  %i41 = load i200, ptr @.str.2, align 1
  %i42.c = icmp ne i200 %i40, %i41
  br i1 %i42.c, label %bb21, label %bb22
bb21:
  call void @"peeper_rt_v1_exit"(i32 30)
  br label %bb22
bb22:
  call void @"peeper_rt_v1_exit"(i32 0)
  ret void
}

declare zeroext i8 @"c_b"(i8 zeroext)

declare { i64, i64 } @"c_many"(i64, i64, i64, i64, i32, ptr byval([16 x i8]) align 8, ptr byval([16 x i8]) align 8, i64)

declare void @"c_mix"(ptr sret([32 x i8]) align 8, i64, i64, double, i64, i64, ptr byval([16 x i8]) align 8, double, i32, ptr byval([16 x i8]) align 8)

declare { i64, i64 } @"c_s100"(i64, i64)

declare i128 @"c_s128"(i128)

declare void @"c_s200"(ptr sret([32 x i8]) align 8, ptr byval([32 x i8]) align 8, i64, i64)

declare i64 @"c_s33"(i64)

declare signext i8 @"c_s5"(i8 signext)

declare signext i8 @"c_s8"(i8 signext)

declare zeroext i16 @"c_u16"(i16 zeroext)

declare { i64, i64 } @"c_u65"(i64, i64)

declare void @"peeper_rt_v1_exit"(i32)

