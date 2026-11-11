source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [65 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:7:12\0A"
@.str.1 = private unnamed_addr constant [70 x i8] c"panic: array literal needs exactly 4 values\0A  --> src/main.peep:11:12\0A"
@.str.2 = private unnamed_addr constant [53 x i8] c"panic: slice out of bounds\0A  --> src/main.peep:18:32\0A"
@.str.3 = private unnamed_addr constant [60 x i8] c"panic: slice starts after it ends\0A  --> src/main.peep:18:24\0A"
@.str.4 = private unnamed_addr constant [53 x i8] c"panic: slice out of bounds\0A  --> src/main.peep:19:36\0A"

define i32 @"ll_slicing:main.total"(ptr %a0) !dbg !11 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a0, metadata !12, metadata !DIExpression()), !dbg !13
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !14
  %i0 = load i64, ptr %i0.a, !dbg !14
  %i1.c = icmp ule i64 %i0, 0, !dbg !14
  br i1 %i1.c, label %bb0.1.panic, label %bb0.1, !dbg !14
bb0.1:
  %i3.a = load ptr, ptr %a0, !dbg !14
  %i3 = load i32, ptr %i3.a, !dbg !14
  %i4.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !15
  %i4 = load i64, ptr %i4.a, !dbg !15
  %i5 = trunc i64 %i4 to i32, !dbg !15
  %i6 = add i32 %i3, %i5, !dbg !14
  ret i32 %i6, !dbg !16
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 65), !dbg !14
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !14
  unreachable
}

define void @"ll_slicing:main.spread"(ptr %a0, ptr %a1) !dbg !29 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a1, metadata !30, metadata !DIExpression()), !dbg !31
  %s1 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a1, i64 8, !dbg !32
  %i0 = load i64, ptr %i0.a, !dbg !32
  %i1 = zext i64 %i0 to i128, !dbg !32
  %i2.c = icmp ult i128 4, %i1, !dbg !32
  %i2 = zext i1 %i2.c to i8, !dbg !32
  %i3 = or i8 0, %i2, !dbg !32
  %i4 = sub i128 4, %i1, !dbg !32
  %i5.c = icmp ne i128 %i4, 0, !dbg !32
  %i5 = zext i1 %i5.c to i8, !dbg !32
  %i6 = or i8 %i3, %i5, !dbg !32
  %i7.c = icmp ne i8 %i6, 0, !dbg !32
  br i1 %i7.c, label %bb0.1.panic, label %bb0.1, !dbg !32
bb0.1:
  %i8 = trunc i128 %i1 to i64, !dbg !32
  store i64 0, ptr %s1, !dbg !32
  br label %bb1
bb1:
  %i10 = load i64, ptr %s1, !dbg !32
  %i11.c = icmp ult i64 %i10, %i8, !dbg !32
  br i1 %i11.c, label %bb2, label %bb3, !dbg !32
bb2:
  %i12.t = getelementptr inbounds [8 x i8], ptr %a0, i64 %i10, !dbg !32
  %i12.f.1 = load ptr, ptr %a1, !dbg !32
  %i12.f = getelementptr inbounds [8 x i8], ptr %i12.f.1, i64 %i10, !dbg !32
  call void @llvm.memcpy.p0.p0.i64(ptr %i12.t, ptr %i12.f, i64 8, i1 false), !dbg !32
  %i13 = add i64 %i10, 1, !dbg !32
  store i64 %i13, ptr %s1, !dbg !32
  br label %bb1
bb3:
  %i15 = trunc i128 %i1 to i64, !dbg !32
  %i16 = add i64 0, %i15, !dbg !32
  ret void, !dbg !33
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 70), !dbg !32
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !32
  unreachable
}

define void @"ll_slicing:main.main"() !dbg !35 {
entry:
  %s0 = alloca [16 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !39, metadata !DIExpression()), !dbg !40
  %s1 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !41, metadata !DIExpression()), !dbg !42
  %s2 = alloca [8 x i8], align 8
  call void @llvm.dbg.declare(metadata ptr %s2, metadata !44, metadata !DIExpression()), !dbg !45
  %s3 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s3, metadata !46, metadata !DIExpression()), !dbg !47
  %s4 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s4, metadata !48, metadata !DIExpression()), !dbg !49
  %s5 = alloca [16 x i8], align 8
  %s6 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  store i32 1, ptr %s0, !dbg !50
  %i1 = getelementptr inbounds i8, ptr %s0, i64 4, !dbg !50
  store i32 2, ptr %i1, !dbg !50
  %i2 = getelementptr inbounds i8, ptr %s0, i64 8, !dbg !50
  store i32 3, ptr %i2, !dbg !50
  %i3 = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !50
  store i32 4, ptr %i3, !dbg !50
  store i32 1, ptr %s1, !dbg !51
  store i64 2, ptr %s2, !dbg !52
  %i6 = load i32, ptr %s1, !dbg !53
  %i7 = sext i32 %i6 to i64, !dbg !53
  %i8.c = icmp ult i64 4, %i7, !dbg !53
  br i1 %i8.c, label %bb0.1.panic, label %bb0.1, !dbg !53
bb0.1:
  %i10.c = icmp ult i64 3, %i7, !dbg !54
  br i1 %i10.c, label %bb0.2.panic, label %bb0.2, !dbg !54
bb0.2:
  %i12 = sub i64 3, %i7, !dbg !54
  %i13 = getelementptr inbounds [4 x i8], ptr %s0, i64 %i7, !dbg !54
  store ptr %i13, ptr %s5, !dbg !54
  %i15 = getelementptr inbounds i8, ptr %s5, i64 8, !dbg !54
  store i64 %i12, ptr %i15, !dbg !54
  %i17 = call i32 @"ll_slicing:main.total"(ptr %s5), !dbg !55
  store i32 %i17, ptr %s3, !dbg !56
  %i20 = load i64, ptr %s2, !dbg !57
  %i21.c = icmp ule i64 4, %i20, !dbg !57
  br i1 %i21.c, label %bb0.3.panic, label %bb0.3, !dbg !57
bb0.3:
  %i23 = add i64 %i20, 1, !dbg !58
  store ptr %s0, ptr %s6, !dbg !58
  %i26 = getelementptr inbounds i8, ptr %s6, i64 8, !dbg !58
  store i64 %i23, ptr %i26, !dbg !58
  %i28 = call i32 @"ll_slicing:main.total"(ptr %s6), !dbg !59
  store i32 %i28, ptr %s4, !dbg !60
  ret void, !dbg !61
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.2, i32 53), !dbg !53
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !53
  unreachable
bb0.2.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.3, i32 60), !dbg !54
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !54
  unreachable
bb0.3.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.4, i32 53), !dbg !57
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !57
  unreachable
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!62, !63}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!5 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !6, offset: 64)
!8 = !{!5, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&[]i32", size: 128, elements: !8)
!6 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!9 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !3, size: 64)
!10 = !DISubroutineType(types: !{!2, !9})
!11 = distinct !DISubprogram(name: "total", linkageName: "ll_slicing:main.total", scope: !0, file: !0, line: 6, type: !10, scopeLine: 6, spFlags: DISPFlagDefinition, unit: !1)
!12 = !DILocalVariable(name: "view", arg: 1, scope: !11, file: !0, line: 6, type: !3)
!13 = !DILocation(line: 6, column: 10, scope: !11)
!14 = !DILocation(line: 7, column: 12, scope: !11)
!15 = !DILocation(line: 7, column: 22, scope: !11)
!16 = !DILocation(line: 7, column: 5, scope: !11)
!19 = !DISubrange(count: 4)
!17 = !DICompositeType(tag: DW_TAG_array_type, baseType: !18, size: 256, elements: !{!19})
!18 = !DIBasicType(name: "i64", size: 64, encoding: DW_ATE_signed)
!20 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !17, size: 64)
!23 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !21, baseType: !22, offset: 0)
!25 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !21, baseType: !24, offset: 64)
!26 = !{!23, !25}
!21 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&[]i64", size: 128, elements: !26)
!24 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!22 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !18, size: 64)
!27 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !21, size: 64)
!28 = !DISubroutineType(types: !{null, !20, !27})
!29 = distinct !DISubprogram(name: "spread", linkageName: "ll_slicing:main.spread", scope: !0, file: !0, line: 10, type: !28, scopeLine: 10, spFlags: DISPFlagDefinition, unit: !1)
!30 = !DILocalVariable(name: "view", arg: 1, scope: !29, file: !0, line: 10, type: !21)
!31 = !DILocation(line: 10, column: 11, scope: !29)
!32 = !DILocation(line: 11, column: 12, scope: !29)
!33 = !DILocation(line: 11, column: 5, scope: !29)
!34 = !DISubroutineType(types: !{null})
!35 = distinct !DISubprogram(name: "main", linkageName: "ll_slicing:main.main", scope: !0, file: !0, line: 14, type: !34, scopeLine: 14, spFlags: DISPFlagDefinition, unit: !1)
!36 = !DILexicalBlock(scope: !35, file: !0, line: 14, column: 11)
!38 = !DISubrange(count: 4)
!37 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 128, elements: !{!38})
!39 = !DILocalVariable(name: "values", scope: !36, file: !0, line: 15, type: !37)
!40 = !DILocation(line: 15, column: 9, scope: !36)
!41 = !DILocalVariable(name: "start", scope: !36, file: !0, line: 16, type: !2)
!42 = !DILocation(line: 16, column: 9, scope: !36)
!43 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!44 = !DILocalVariable(name: "end", scope: !36, file: !0, line: 17, type: !43)
!45 = !DILocation(line: 17, column: 9, scope: !36)
!46 = !DILocalVariable(name: "middle", scope: !36, file: !0, line: 18, type: !2)
!47 = !DILocation(line: 18, column: 9, scope: !36)
!48 = !DILocalVariable(name: "through", scope: !36, file: !0, line: 19, type: !2)
!49 = !DILocation(line: 19, column: 9, scope: !36)
!50 = !DILocation(line: 15, column: 18, scope: !36)
!51 = !DILocation(line: 16, column: 5, scope: !36)
!52 = !DILocation(line: 17, column: 5, scope: !36)
!53 = !DILocation(line: 18, column: 32, scope: !36)
!54 = !DILocation(line: 18, column: 24, scope: !36)
!55 = !DILocation(line: 18, column: 18, scope: !36)
!56 = !DILocation(line: 18, column: 5, scope: !36)
!57 = !DILocation(line: 19, column: 36, scope: !36)
!58 = !DILocation(line: 19, column: 25, scope: !36)
!59 = !DILocation(line: 19, column: 19, scope: !36)
!60 = !DILocation(line: 19, column: 5, scope: !36)
!61 = !DILocation(line: 20, column: 1, scope: !36)
!62 = !{i32 7, !"Dwarf Version", i32 4}
!63 = !{i32 2, !"Debug Info Version", i32 3}
