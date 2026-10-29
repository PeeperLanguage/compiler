source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

define void @"ll_extern_integers:main.main"() {
entry:
  %i14.r = alloca [16 x i8], align 16
  %i17.r = alloca [16 x i8], align 16
  %i23.0 = alloca [32 x i8], align 8
  %i23.r = alloca [32 x i8], align 8
  %i26.3 = alloca [16 x i8], align 8
  %i26.4 = alloca [16 x i8], align 8
  %i26.r = alloca [16 x i8], align 16
  %i29.3 = alloca [16 x i8], align 8
  %i29.6 = alloca [16 x i8], align 8
  %i29.r = alloca [32 x i8], align 8
  br label %bb0
bb0:
  %i0 = call zeroext i1 @"c_b"(i1 zeroext true)
  br i1 %i0, label %bb1, label %bb2
bb1:
  call void @"peeper_rt_v1_exit"(i32 20)
  br label %bb2
bb2:
  %i2 = call signext i8 @"c_s8"(i8 signext -3)
  %i3 = icmp ne i8 %i2, -4
  br i1 %i3, label %bb3, label %bb4
bb3:
  call void @"peeper_rt_v1_exit"(i32 21)
  br label %bb4
bb4:
  %i5 = call zeroext i16 @"c_u16"(i16 zeroext 65000)
  %i6 = icmp ne i16 %i5, 65001
  br i1 %i6, label %bb5, label %bb6
bb5:
  call void @"peeper_rt_v1_exit"(i32 22)
  br label %bb6
bb6:
  %i8 = call signext i5 @"c_s5"(i5 signext -7)
  %i9 = icmp ne i5 %i8, -8
  br i1 %i9, label %bb7, label %bb8
bb7:
  call void @"peeper_rt_v1_exit"(i32 23)
  br label %bb8
bb8:
  %i11 = call i33 @"c_s33"(i33 -2147483643)
  %i12 = icmp ne i33 %i11, 2147483642
  br i1 %i12, label %bb9, label %bb10
bb9:
  call void @"peeper_rt_v1_exit"(i32 24)
  br label %bb10
bb10:
  %i14.0.x = zext i65 u0x00000000000000010000000000000003 to i128
  %i14.0.s0 = lshr i128 %i14.0.x, 0
  %i14.0.l0 = trunc i128 %i14.0.s0 to i64
  %i14.0.s1 = lshr i128 %i14.0.x, 64
  %i14.0.l1 = trunc i128 %i14.0.s1 to i64
  %i14.c = call { i64, i64 } @"c_u65"(i64 %i14.0.l0, i64 %i14.0.l1)
  store { i64, i64 } %i14.c, ptr %i14.r, align 16
  %i14.w = load i128, ptr %i14.r, align 16
  %i14 = trunc i128 %i14.w to i65
  %i15 = icmp ne i65 %i14, u0x00000000000000010000000000000004
  br i1 %i15, label %bb11, label %bb12
bb11:
  call void @"peeper_rt_v1_exit"(i32 25)
  br label %bb12
bb12:
  %i17.0.x = sext i100 u0x0000000FFFFFFFBFFFFFFFFFFFFFFFF7 to i128
  %i17.0.s0 = lshr i128 %i17.0.x, 0
  %i17.0.l0 = trunc i128 %i17.0.s0 to i64
  %i17.0.s1 = lshr i128 %i17.0.x, 64
  %i17.0.l1 = trunc i128 %i17.0.s1 to i64
  %i17.c = call { i64, i64 } @"c_s100"(i64 %i17.0.l0, i64 %i17.0.l1)
  store { i64, i64 } %i17.c, ptr %i17.r, align 16
  %i17.w = load i128, ptr %i17.r, align 16
  %i17 = trunc i128 %i17.w to i100
  %i18 = icmp ne i100 %i17, u0x00000000040000000000000000000001
  br i1 %i18, label %bb13, label %bb14
bb13:
  call void @"peeper_rt_v1_exit"(i32 26)
  br label %bb14
bb14:
  %i20 = call i128 @"c_s128"(i128 u0xFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFF)
  %i21 = icmp ne i128 %i20, u0x01000000000000000000000000000007
  br i1 %i21, label %bb15, label %bb16
bb15:
  call void @"peeper_rt_v1_exit"(i32 27)
  br label %bb16
bb16:
  %i23.0.x = sext i200 u0x00000000000000FFFFFFFFFFFFBFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFE to i256
  store i256 %i23.0.x, ptr %i23.0, align 8
  %i23.1.x = sext i65 u0x00000000000000017FFFFFFFFFFFFFFF to i128
  %i23.1.s0 = lshr i128 %i23.1.x, 0
  %i23.1.l0 = trunc i128 %i23.1.s0 to i64
  %i23.1.s1 = lshr i128 %i23.1.x, 64
  %i23.1.l1 = trunc i128 %i23.1.s1 to i64
  call void @"c_s200"(ptr sret([32 x i8]) align 8 %i23.r, ptr byval([32 x i8]) align 8 %i23.0, i64 %i23.1.l0, i64 %i23.1.l1)
  %i23.w = load i256, ptr %i23.r, align 8
  %i23 = trunc i256 %i23.w to i200
  %i24 = icmp ne i200 %i23, u0x000000000000000040000000000000000000000000000000000000000000000B
  br i1 %i24, label %bb17, label %bb18
bb17:
  call void @"peeper_rt_v1_exit"(i32 28)
  br label %bb18
bb18:
  %i26.0.x = sext i65 u0x00000000000000000000000000000001 to i128
  %i26.0.s0 = lshr i128 %i26.0.x, 0
  %i26.0.l0 = trunc i128 %i26.0.s0 to i64
  %i26.0.s1 = lshr i128 %i26.0.x, 64
  %i26.0.l1 = trunc i128 %i26.0.s1 to i64
  %i26.1.x = sext i65 u0x0000000000000001FFFFFFFFFFFFFFFE to i128
  %i26.1.s0 = lshr i128 %i26.1.x, 0
  %i26.1.l0 = trunc i128 %i26.1.s0 to i64
  %i26.1.s1 = lshr i128 %i26.1.x, 64
  %i26.1.l1 = trunc i128 %i26.1.s1 to i64
  %i26.3.x = sext i65 u0x0000000000000000FFFFFFFFFFFFFFFF to i128
  store i128 %i26.3.x, ptr %i26.3, align 8
  %i26.4.x = sext i65 u0x0000000000000001FFFFFFFFFFFFFFFB to i128
  store i128 %i26.4.x, ptr %i26.4, align 8
  %i26.c = call { i64, i64 } @"c_many"(i64 %i26.0.l0, i64 %i26.0.l1, i64 %i26.1.l0, i64 %i26.1.l1, i32 3, ptr byval([16 x i8]) align 8 %i26.3, ptr byval([16 x i8]) align 8 %i26.4, i64 6)
  store { i64, i64 } %i26.c, ptr %i26.r, align 16
  %i26.w = load i128, ptr %i26.r, align 16
  %i26 = trunc i128 %i26.w to i65
  %i27 = icmp ne i65 %i26, u0x00000000000000017FFFFFFFFFFFFFF9
  br i1 %i27, label %bb19, label %bb20
bb19:
  call void @"peeper_rt_v1_exit"(i32 29)
  br label %bb20
bb20:
  %i29.0.x = sext i65 u0x0000000000000001FFFFFFFFFFFFFFFF to i128
  %i29.0.s0 = lshr i128 %i29.0.x, 0
  %i29.0.l0 = trunc i128 %i29.0.s0 to i64
  %i29.0.s1 = lshr i128 %i29.0.x, 64
  %i29.0.l1 = trunc i128 %i29.0.s1 to i64
  %i29.2.x = sext i65 u0x00000000000000000000000000000002 to i128
  %i29.2.s0 = lshr i128 %i29.2.x, 0
  %i29.2.l0 = trunc i128 %i29.2.s0 to i64
  %i29.2.s1 = lshr i128 %i29.2.x, 64
  %i29.2.l1 = trunc i128 %i29.2.s1 to i64
  %i29.3.x = sext i65 u0x00000000000000010000000000000000 to i128
  store i128 %i29.3.x, ptr %i29.3, align 8
  %i29.6.x = zext i65 u0x00000000000000010000000000000001 to i128
  store i128 %i29.6.x, ptr %i29.6, align 8
  call void @"c_mix"(ptr sret([32 x i8]) align 8 %i29.r, i64 %i29.0.l0, i64 %i29.0.l1, double 0x3FF8000000000000, i64 %i29.2.l0, i64 %i29.2.l1, ptr byval([16 x i8]) align 8 %i29.3, double 0x4004000000000000, i32 -9, ptr byval([16 x i8]) align 8 %i29.6)
  %i29.w = load i256, ptr %i29.r, align 8
  %i29 = trunc i256 %i29.w to i200
  %i30 = icmp ne i200 %i29, u0x000000000000008000000000000000000000000000000000000000000000000D
  br i1 %i30, label %bb21, label %bb22
bb21:
  call void @"peeper_rt_v1_exit"(i32 30)
  br label %bb22
bb22:
  call void @"peeper_rt_v1_exit"(i32 0)
  ret void
}

declare zeroext i1 @"c_b"(i1 zeroext)

declare { i64, i64 } @"c_many"(i64, i64, i64, i64, i32, ptr byval([16 x i8]) align 8, ptr byval([16 x i8]) align 8, i64)

declare void @"c_mix"(ptr sret([32 x i8]) align 8, i64, i64, double, i64, i64, ptr byval([16 x i8]) align 8, double, i32, ptr byval([16 x i8]) align 8)

declare { i64, i64 } @"c_s100"(i64, i64)

declare i128 @"c_s128"(i128)

declare void @"c_s200"(ptr sret([32 x i8]) align 8, ptr byval([32 x i8]) align 8, i64, i64)

declare i33 @"c_s33"(i33)

declare signext i5 @"c_s5"(i5 signext)

declare signext i8 @"c_s8"(i8 signext)

declare zeroext i16 @"c_u16"(i16 zeroext)

declare { i64, i64 } @"c_u65"(i64, i64)

declare void @"peeper_rt_v1_exit"(i32)

