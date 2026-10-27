source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [118 x i8] c"panic: index out of bounds of the slice\0A  --> <fixture>/src/main.peep:7:12\0A\00"
@.str.1 = private unnamed_addr constant [123 x i8] c"panic: array literal needs exactly 4 values\0A  --> <fixture>/src/main.peep:11:12\0A\00"
@.str.2 = private unnamed_addr constant [106 x i8] c"panic: slice out of bounds\0A  --> <fixture>/src/main.peep:18:32\0A\00"
@.str.3 = private unnamed_addr constant [113 x i8] c"panic: slice starts after it ends\0A  --> <fixture>/src/main.peep:18:24\0A\00"
@.str.4 = private unnamed_addr constant [106 x i8] c"panic: slice out of bounds\0A  --> <fixture>/src/main.peep:19:36\0A\00"

define i32 @"ll_slicing:main.total"(%slice %arg0) !dbg !10 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !11, metadata !DIExpression()), !dbg !12
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !12
  %i1.f.0.f = getelementptr inbounds %slice, ptr %v0, i32 0, !dbg !13
  %i1.f.0.s = load %slice, ptr %i1.f.0.f, !dbg !13
  %i1.f.0 = extractvalue %slice %i1.f.0.s, 0, !dbg !13
  %i1.f.0.len = extractvalue %slice %i1.f.0.s, 1, !dbg !13
  %i1.f.1.0.out = icmp uge i64 0, %i1.f.0.len, !dbg !13
  br i1 %i1.f.1.0.out, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i1.f = getelementptr inbounds i32, ptr %i1.f.0, i64 0, !dbg !13
  %i1 = load i32, ptr %i1.f, !dbg !13
  %i2 = load %slice, ptr %v0, !dbg !14
  %i3 = extractvalue %slice %i2, 1, !dbg !14
  %i4 = trunc i64 %i3 to i32, !dbg !14
  %i5 = add i32 %i1, %i4, !dbg !13
  ret i32 %i5, !dbg !15
bb1:
  unreachable
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 117), !dbg !13
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !13
  unreachable
}

define void @"ll_slicing:main.spread"(ptr %result, %slice %arg0) !dbg !27 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !28, metadata !DIExpression()), !dbg !29
  %i2 = alloca [4 x i64]
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !29
  %i1 = load %slice, ptr %v0, !dbg !30
  %i2.r0.len = extractvalue %slice %i1, 1, !dbg !31
  %i2.r0.count = zext i64 %i2.r0.len to i65, !dbg !31
  %i2.r0.over = icmp ugt i65 %i2.r0.count, 4, !dbg !31
  %i2.r0.failed = or i1 false, %i2.r0.over, !dbg !31
  %i2.r0.left.after = sub i65 4, %i2.r0.count, !dbg !31
  %i2.short = icmp ne i65 %i2.r0.left.after, 0, !dbg !31
  %i2.failed = or i1 %i2.r0.failed, %i2.short, !dbg !31
  br i1 %i2.failed, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i2.0 = getelementptr inbounds [4 x i64], ptr %i2, i32 0, i64 0, !dbg !31
  %i2.s0 = extractvalue %slice %i1, 0, !dbg !31
  %i2.s0.bytes = mul i64 %i2.r0.len, 8, !dbg !31
  call void @llvm.memcpy.p0.p0.i64(ptr %i2.0, ptr %i2.s0, i64 %i2.s0.bytes, i1 false), !dbg !31
  call void @llvm.memcpy.p0.p0.i64(ptr %result, ptr %i2, i64 32, i1 false), !dbg !32
  ret void, !dbg !32
bb1:
  unreachable
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 122), !dbg !31
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !31
  unreachable
}

define void @"ll_slicing:main.main"() !dbg !34 {
entry:
  %v0 = alloca [4 x i32]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !38, metadata !DIExpression()), !dbg !39
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !40, metadata !DIExpression()), !dbg !41
  %v2 = alloca i64
  call void @llvm.dbg.declare(metadata ptr %v2, metadata !43, metadata !DIExpression()), !dbg !44
  %v3 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v3, metadata !45, metadata !DIExpression()), !dbg !46
  %v4 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v4, metadata !47, metadata !DIExpression()), !dbg !48
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 0, !dbg !49
  store i32 1, ptr %i0.0, !dbg !49
  %i0.1 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 1, !dbg !49
  store i32 2, ptr %i0.1, !dbg !49
  %i0.2 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 2, !dbg !49
  store i32 3, ptr %i0.2, !dbg !49
  %i0.3 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 3, !dbg !49
  store i32 4, ptr %i0.3, !dbg !49
  store i32 1, ptr %v1, !dbg !51
  store i64 2, ptr %v2, !dbg !52
  %i4 = load i32, ptr %v1, !dbg !53
  %i5 = sext i32 %i4 to i64, !dbg !53
  %i6 = icmp ugt i64 %i5, 4, !dbg !53
  br i1 %i6, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i8 = icmp ugt i64 %i5, 3, !dbg !54
  br i1 %i8, label %bb0.2.panic, label %bb0.2
bb0.2:
  %i10 = sub i64 3, %i5, !dbg !54
  %i11.p = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 %i5, !dbg !54
  %i11.a = insertvalue %slice poison, ptr %i11.p, 0, !dbg !54
  %i11 = insertvalue %slice %i11.a, i64 %i10, 1, !dbg !54
  %i12 = call i32 @"ll_slicing:main.total"(%slice %i11), !dbg !55
  store i32 %i12, ptr %v3, !dbg !56
  %i14 = load i64, ptr %v2, !dbg !57
  %i15 = icmp uge i64 %i14, 4, !dbg !57
  br i1 %i15, label %bb0.3.panic, label %bb0.3
bb0.3:
  %i17 = add i64 %i14, 1, !dbg !58
  %i18.p = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 0, !dbg !58
  %i18.a = insertvalue %slice poison, ptr %i18.p, 0, !dbg !58
  %i18 = insertvalue %slice %i18.a, i64 %i17, 1, !dbg !58
  %i19 = call i32 @"ll_slicing:main.total"(%slice %i18), !dbg !59
  store i32 %i19, ptr %v4, !dbg !60
  ret void, !dbg !61
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.2, i32 105), !dbg !53
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !53
  unreachable
bb0.2.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.3, i32 112), !dbg !54
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !54
  unreachable
bb0.3.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.4, i32 105), !dbg !57
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
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!5 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!6 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !5, offset: 64)
!8 = !{!6, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "[&]i32", size: 128, elements: !8)
!9 = !DISubroutineType(types: !{!2, !3})
!10 = distinct !DISubprogram(name: "total", linkageName: "ll_slicing:main.total", scope: !0, file: !0, line: 6, type: !9, scopeLine: 6, spFlags: DISPFlagDefinition, unit: !1)
!11 = !DILocalVariable(name: "view", arg: 1, scope: !10, file: !0, line: 6, type: !3)
!12 = !DILocation(line: 6, column: 10, scope: !10)
!13 = !DILocation(line: 7, column: 12, scope: !10)
!14 = !DILocation(line: 7, column: 22, scope: !10)
!15 = !DILocation(line: 7, column: 5, scope: !10)
!16 = !DIBasicType(name: "i64", size: 64, encoding: DW_ATE_signed)
!17 = !DISubrange(count: 4)
!18 = !DICompositeType(tag: DW_TAG_array_type, baseType: !16, size: 256, elements: !{!17})
!19 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !18, size: 64)
!21 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !16, size: 64)
!22 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!23 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !20, baseType: !21, offset: 0)
!24 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !20, baseType: !22, offset: 64)
!25 = !{!23, !24}
!20 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "[&]i64", size: 128, elements: !25)
!26 = !DISubroutineType(types: !{null, !19, !20})
!27 = distinct !DISubprogram(name: "spread", linkageName: "ll_slicing:main.spread", scope: !0, file: !0, line: 10, type: !26, scopeLine: 10, spFlags: DISPFlagDefinition, unit: !1)
!28 = !DILocalVariable(name: "view", arg: 1, scope: !27, file: !0, line: 10, type: !20)
!29 = !DILocation(line: 10, column: 11, scope: !27)
!30 = !DILocation(line: 11, column: 19, scope: !27)
!31 = !DILocation(line: 11, column: 12, scope: !27)
!32 = !DILocation(line: 11, column: 5, scope: !27)
!33 = !DISubroutineType(types: !{null})
!34 = distinct !DISubprogram(name: "main", linkageName: "ll_slicing:main.main", scope: !0, file: !0, line: 14, type: !33, scopeLine: 14, spFlags: DISPFlagDefinition, unit: !1)
!35 = !DILexicalBlock(scope: !34, file: !0, line: 14, column: 11)
!36 = !DISubrange(count: 4)
!37 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 128, elements: !{!36})
!38 = !DILocalVariable(name: "values", scope: !35, file: !0, line: 15, type: !37)
!39 = !DILocation(line: 15, column: 9, scope: !35)
!40 = !DILocalVariable(name: "start", scope: !35, file: !0, line: 16, type: !2)
!41 = !DILocation(line: 16, column: 9, scope: !35)
!42 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!43 = !DILocalVariable(name: "end", scope: !35, file: !0, line: 17, type: !42)
!44 = !DILocation(line: 17, column: 9, scope: !35)
!45 = !DILocalVariable(name: "middle", scope: !35, file: !0, line: 18, type: !2)
!46 = !DILocation(line: 18, column: 9, scope: !35)
!47 = !DILocalVariable(name: "through", scope: !35, file: !0, line: 19, type: !2)
!48 = !DILocation(line: 19, column: 9, scope: !35)
!49 = !DILocation(line: 15, column: 18, scope: !35)
!50 = !DILocation(line: 15, column: 5, scope: !35)
!51 = !DILocation(line: 16, column: 5, scope: !35)
!52 = !DILocation(line: 17, column: 5, scope: !35)
!53 = !DILocation(line: 18, column: 32, scope: !35)
!54 = !DILocation(line: 18, column: 24, scope: !35)
!55 = !DILocation(line: 18, column: 18, scope: !35)
!56 = !DILocation(line: 18, column: 5, scope: !35)
!57 = !DILocation(line: 19, column: 36, scope: !35)
!58 = !DILocation(line: 19, column: 25, scope: !35)
!59 = !DILocation(line: 19, column: 19, scope: !35)
!60 = !DILocation(line: 19, column: 5, scope: !35)
!61 = !DILocation(line: 20, column: 1, scope: !35)
!62 = !{i32 7, !"Dwarf Version", i32 4}
!63 = !{i32 2, !"Debug Info Version", i32 3}
