source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [1 x i8] c" "
@.str.1 = private unnamed_addr constant [1 x i8] c"\0A"

define void @"ll_array_ranges:main.known"(ptr %result) !dbg !7 {
entry:
  %s0 = alloca [24 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !8, metadata !DIExpression()), !dbg !9
  %s1 = alloca [4 x i8], align 4
  %s2 = alloca [8 x i8], align 8
  %s3 = alloca [4 x i8], align 4
  %s4 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store i32 0, ptr %s1, !dbg !10
  store i64 0, ptr %s2, !dbg !10
  br label %bb1
bb1:
  %i2 = load i64, ptr %s2, !dbg !10
  %i3.c = icmp ult i64 %i2, 3, !dbg !10
  br i1 %i3.c, label %bb2, label %bb3, !dbg !10
bb2:
  %i4 = load i32, ptr %s1, !dbg !10
  %i5 = getelementptr inbounds [4 x i8], ptr %s0, i64 %i2, !dbg !10
  store i32 %i4, ptr %i5, !dbg !10
  %i6 = add i32 %i4, 1, !dbg !10
  store i32 %i6, ptr %s1, !dbg !10
  %i8 = add i64 %i2, 1, !dbg !10
  store i64 %i8, ptr %s2, !dbg !10
  br label %bb1
bb3:
  %i10 = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !10
  store i32 9, ptr %i10, !dbg !10
  store i32 5, ptr %s3, !dbg !10
  store i64 0, ptr %s4, !dbg !10
  br label %bb4
bb4:
  %i13 = load i64, ptr %s4, !dbg !10
  %i14.c = icmp ult i64 %i13, 2, !dbg !10
  br i1 %i14.c, label %bb5, label %bb6, !dbg !10
bb5:
  %i15 = load i32, ptr %s3, !dbg !10
  %i16.1 = getelementptr inbounds i8, ptr %s0, i64 16, !dbg !10
  %i16 = getelementptr inbounds [4 x i8], ptr %i16.1, i64 %i13, !dbg !10
  store i32 %i15, ptr %i16, !dbg !10
  %i17 = add i32 %i15, -1, !dbg !10
  store i32 %i17, ptr %s3, !dbg !10
  %i19 = add i64 %i13, 1, !dbg !10
  store i64 %i19, ptr %s4, !dbg !10
  br label %bb4
bb6:
  call void @llvm.memcpy.p0.p0.i64(ptr %result, ptr %s0, i64 24, i1 false), !dbg !11
  ret void, !dbg !12
}

define void @"ll_array_ranges:main.main"() !dbg !14 {
entry:
  %s0 = alloca [24 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !15, metadata !DIExpression()), !dbg !16
  %s1 = alloca [8 x i8], align 8
  %s2 = alloca [8 x i8], align 8
  %s3 = alloca [8 x i8], align 8
  %s4 = alloca [8 x i8], align 8
  %s5 = alloca [8 x i8], align 8
  %s6 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  call void @"ll_array_ranges:main.known"(ptr %s0), !dbg !17
  %i1 = load i32, ptr %s0, !dbg !18
  %i2.a = getelementptr inbounds i8, ptr %s0, i64 4, !dbg !19
  %i2 = load i32, ptr %i2.a, !dbg !19
  %i3.a = getelementptr inbounds i8, ptr %s0, i64 8, !dbg !20
  %i3 = load i32, ptr %i3.a, !dbg !20
  %i4.a = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !21
  %i4 = load i32, ptr %i4.a, !dbg !21
  %i5.a = getelementptr inbounds i8, ptr %s0, i64 16, !dbg !22
  %i5 = load i32, ptr %i5.a, !dbg !22
  %i6.a = getelementptr inbounds i8, ptr %s0, i64 20, !dbg !23
  %i6 = load i32, ptr %i6.a, !dbg !23
  %i7 = sext i32 %i1 to i64, !dbg !24
  store i64 %i7, ptr %s1, !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s1), !dbg !24
  %i11 = sext i32 %i2 to i64, !dbg !24
  store i64 %i11, ptr %s2, !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s2), !dbg !24
  %i16 = sext i32 %i3 to i64, !dbg !24
  store i64 %i16, ptr %s3, !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s3), !dbg !24
  %i21 = sext i32 %i4 to i64, !dbg !24
  store i64 %i21, ptr %s4, !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s4), !dbg !24
  %i26 = sext i32 %i5 to i64, !dbg !24
  store i64 %i26, ptr %s5, !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s5), !dbg !24
  %i31 = sext i32 %i6 to i64, !dbg !24
  store i64 %i31, ptr %s6, !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !24
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s6), !dbg !24
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 1), !dbg !24
  call void @"peeper_rt_v1_flush"(), !dbg !24
  ret void, !dbg !25
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!26, !27}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!4 = !DISubrange(count: 6)
!2 = !DICompositeType(tag: DW_TAG_array_type, baseType: !3, size: 192, elements: !{!4})
!3 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!5 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!6 = !DISubroutineType(types: !{null, !5})
!7 = distinct !DISubprogram(name: "known", linkageName: "ll_array_ranges:main.known", scope: !0, file: !0, line: 5, type: !6, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!8 = !DILocalVariable(name: "arr", scope: !7, file: !0, line: 6, type: !2)
!9 = !DILocation(line: 6, column: 9, scope: !7)
!10 = !DILocation(line: 6, column: 15, scope: !7)
!11 = !DILocation(line: 7, column: 12, scope: !7)
!12 = !DILocation(line: 7, column: 5, scope: !7)
!13 = !DISubroutineType(types: !{null})
!14 = distinct !DISubprogram(name: "main", linkageName: "ll_array_ranges:main.main", scope: !0, file: !0, line: 10, type: !13, scopeLine: 10, spFlags: DISPFlagDefinition, unit: !1)
!15 = !DILocalVariable(name: "arr", scope: !14, file: !0, line: 11, type: !2)
!16 = !DILocation(line: 11, column: 9, scope: !14)
!17 = !DILocation(line: 11, column: 15, scope: !14)
!18 = !DILocation(line: 12, column: 13, scope: !14)
!19 = !DILocation(line: 12, column: 21, scope: !14)
!20 = !DILocation(line: 12, column: 29, scope: !14)
!21 = !DILocation(line: 12, column: 37, scope: !14)
!22 = !DILocation(line: 12, column: 45, scope: !14)
!23 = !DILocation(line: 12, column: 53, scope: !14)
!24 = !DILocation(line: 12, column: 5, scope: !14)
!25 = !DILocation(line: 13, column: 1, scope: !14)
!26 = !{i32 7, !"Dwarf Version", i32 4}
!27 = !{i32 2, !"Debug Info Version", i32 3}
