source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [65 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:9:12\0A"
@.str.1 = private unnamed_addr constant [70 x i8] c"panic: array literal needs exactly 4 values\0A  --> src/main.peep:13:12\0A"
@.str.2 = private unnamed_addr constant [53 x i8] c"panic: slice out of bounds\0A  --> src/main.peep:20:32\0A"
@.str.3 = private unnamed_addr constant [60 x i8] c"panic: slice starts after it ends\0A  --> src/main.peep:20:24\0A"
@.str.4 = private unnamed_addr constant [53 x i8] c"panic: slice out of bounds\0A  --> src/main.peep:21:36\0A"

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

define void @"ll_slicing:main.spread"(ptr %result, ptr %a0) !dbg !29 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a0, metadata !30, metadata !DIExpression()), !dbg !31
  %s1 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !32
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
  %i12.t = getelementptr inbounds [8 x i8], ptr %result, i64 %i10, !dbg !32
  %i12.f.1 = load ptr, ptr %a0, !dbg !32
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
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !38, metadata !DIExpression()), !dbg !39
  %s1 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !40, metadata !DIExpression()), !dbg !41
  %s2 = alloca [8 x i8], align 8
  call void @llvm.dbg.declare(metadata ptr %s2, metadata !43, metadata !DIExpression()), !dbg !44
  %s3 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s3, metadata !45, metadata !DIExpression()), !dbg !46
  %s4 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s4, metadata !47, metadata !DIExpression()), !dbg !48
  %s5 = alloca [16 x i8], align 8
  %s6 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  store i32 1, ptr %s0, !dbg !49
  %i1 = getelementptr inbounds i8, ptr %s0, i64 4, !dbg !49
  store i32 2, ptr %i1, !dbg !49
  %i2 = getelementptr inbounds i8, ptr %s0, i64 8, !dbg !49
  store i32 3, ptr %i2, !dbg !49
  %i3 = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !49
  store i32 4, ptr %i3, !dbg !49
  store i32 1, ptr %s1, !dbg !50
  store i64 2, ptr %s2, !dbg !51
  %i6 = load i32, ptr %s1, !dbg !52
  %i7 = sext i32 %i6 to i64, !dbg !52
  %i8.c = icmp ult i64 4, %i7, !dbg !52
  br i1 %i8.c, label %bb0.1.panic, label %bb0.1, !dbg !52
bb0.1:
  %i10.c = icmp ult i64 3, %i7, !dbg !53
  br i1 %i10.c, label %bb0.2.panic, label %bb0.2, !dbg !53
bb0.2:
  %i12 = sub i64 3, %i7, !dbg !53
  %i13 = getelementptr inbounds [4 x i8], ptr %s0, i64 %i7, !dbg !53
  store ptr %i13, ptr %s5, !dbg !53
  %i15 = getelementptr inbounds i8, ptr %s5, i64 8, !dbg !53
  store i64 %i12, ptr %i15, !dbg !53
  %i17 = call i32 @"ll_slicing:main.total"(ptr %s5), !dbg !54
  store i32 %i17, ptr %s3, !dbg !55
  %i20 = load i64, ptr %s2, !dbg !56
  %i21.c = icmp ule i64 4, %i20, !dbg !56
  br i1 %i21.c, label %bb0.3.panic, label %bb0.3, !dbg !56
bb0.3:
  %i23 = add i64 %i20, 1, !dbg !57
  store ptr %s0, ptr %s6, !dbg !57
  %i26 = getelementptr inbounds i8, ptr %s6, i64 8, !dbg !57
  store i64 %i23, ptr %i26, !dbg !57
  %i28 = call i32 @"ll_slicing:main.total"(ptr %s6), !dbg !58
  store i32 %i28, ptr %s4, !dbg !59
  ret void, !dbg !60
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.2, i32 53), !dbg !52
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !52
  unreachable
bb0.2.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.3, i32 60), !dbg !53
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !53
  unreachable
bb0.3.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.4, i32 53), !dbg !56
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !56
  unreachable
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!61, !62}
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
!11 = distinct !DISubprogram(name: "total", linkageName: "ll_slicing:main.total", scope: !0, file: !0, line: 8, type: !10, scopeLine: 8, spFlags: DISPFlagDefinition, unit: !1)
!12 = !DILocalVariable(name: "view", arg: 1, scope: !11, file: !0, line: 8, type: !3)
!13 = !DILocation(line: 8, column: 10, scope: !11)
!14 = !DILocation(line: 9, column: 12, scope: !11)
!15 = !DILocation(line: 9, column: 22, scope: !11)
!16 = !DILocation(line: 9, column: 5, scope: !11)
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
!29 = distinct !DISubprogram(name: "spread", linkageName: "ll_slicing:main.spread", scope: !0, file: !0, line: 12, type: !28, scopeLine: 12, spFlags: DISPFlagDefinition, unit: !1)
!30 = !DILocalVariable(name: "view", arg: 1, scope: !29, file: !0, line: 12, type: !21)
!31 = !DILocation(line: 12, column: 11, scope: !29)
!32 = !DILocation(line: 13, column: 12, scope: !29)
!33 = !DILocation(line: 13, column: 5, scope: !29)
!34 = !DISubroutineType(types: !{null})
!35 = distinct !DISubprogram(name: "main", linkageName: "ll_slicing:main.main", scope: !0, file: !0, line: 16, type: !34, scopeLine: 16, spFlags: DISPFlagDefinition, unit: !1)
!37 = !DISubrange(count: 4)
!36 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 128, elements: !{!37})
!38 = !DILocalVariable(name: "values", scope: !35, file: !0, line: 17, type: !36)
!39 = !DILocation(line: 17, column: 9, scope: !35)
!40 = !DILocalVariable(name: "start", scope: !35, file: !0, line: 18, type: !2)
!41 = !DILocation(line: 18, column: 9, scope: !35)
!42 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!43 = !DILocalVariable(name: "end", scope: !35, file: !0, line: 19, type: !42)
!44 = !DILocation(line: 19, column: 9, scope: !35)
!45 = !DILocalVariable(name: "middle", scope: !35, file: !0, line: 20, type: !2)
!46 = !DILocation(line: 20, column: 9, scope: !35)
!47 = !DILocalVariable(name: "through", scope: !35, file: !0, line: 21, type: !2)
!48 = !DILocation(line: 21, column: 9, scope: !35)
!49 = !DILocation(line: 17, column: 18, scope: !35)
!50 = !DILocation(line: 18, column: 5, scope: !35)
!51 = !DILocation(line: 19, column: 5, scope: !35)
!52 = !DILocation(line: 20, column: 32, scope: !35)
!53 = !DILocation(line: 20, column: 24, scope: !35)
!54 = !DILocation(line: 20, column: 18, scope: !35)
!55 = !DILocation(line: 20, column: 5, scope: !35)
!56 = !DILocation(line: 21, column: 36, scope: !35)
!57 = !DILocation(line: 21, column: 25, scope: !35)
!58 = !DILocation(line: 21, column: 19, scope: !35)
!59 = !DILocation(line: 21, column: 5, scope: !35)
!60 = !DILocation(line: 22, column: 1, scope: !35)
!61 = !{i32 7, !"Dwarf Version", i32 4}
!62 = !{i32 2, !"Debug Info Version", i32 3}
