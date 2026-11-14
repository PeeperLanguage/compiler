source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [8 x i8] c"scaled\0A\00"

define i32 @"ll_debug_scopes:main.scale"(i32 %a0, i32 %a1) !dbg !4 {
entry:
  %s0 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !8, metadata !DIExpression()), !dbg !9
  %s1 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !10, metadata !DIExpression()), !dbg !11
  %s2 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s2, metadata !12, metadata !DIExpression()), !dbg !13
  %s3 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s3, metadata !14, metadata !DIExpression()), !dbg !15
  %s4 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s4, metadata !16, metadata !DIExpression()), !dbg !17
  %s5 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s5, metadata !18, metadata !DIExpression()), !dbg !19
  br label %bb0
bb0:
  store i32 %a0, ptr %s0, !dbg !9
  store i32 %a1, ptr %s1, !dbg !11
  %i2 = load i32, ptr %s0, !dbg !20
  %i3 = load i32, ptr %s1, !dbg !21
  %i4 = add i32 %i2, %i3, !dbg !20
  store i32 %i4, ptr %s2, !dbg !22
  %i6 = load i32, ptr %s2, !dbg !23
  %i7 = mul i32 %i6, 2, !dbg !23
  store i32 %i7, ptr %s3, !dbg !24
  %i9 = load i32, ptr %s3, !dbg !25
  %i10.c = icmp slt i32 2, %i9, !dbg !25
  br i1 %i10.c, label %bb1, label %bb2, !dbg !25
bb1:
  %i11 = load i32, ptr %s3, !dbg !26
  %i12 = sdiv i32 %i11, 2, !dbg !26
  store i32 %i12, ptr %s4, !dbg !27
  %i14 = load i32, ptr %s4, !dbg !28
  ret i32 %i14, !dbg !29
bb2:
  store i32 3, ptr %s5, !dbg !30
  %i23 = load i32, ptr %s5, !dbg !31
  %i24 = load i32, ptr %s2, !dbg !32
  %i25 = add i32 %i23, %i24, !dbg !31
  ret i32 %i25, !dbg !33
}

define i32 @"ll_debug_scopes:main.main"() !dbg !35 {
entry:
  %s0 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !37, metadata !DIExpression()), !dbg !38
  %s1 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !39, metadata !DIExpression()), !dbg !40
  %s2 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s2, metadata !41, metadata !DIExpression()), !dbg !42
  br label %bb0
bb0:
  %i0 = call i32 @"peeper_rt_v1_write"(i32 1, ptr @.str.0, i32 7), !dbg !43
  %i1 = call i32 @"ll_debug_scopes:main.scale"(i32 1, i32 2), !dbg !44
  store i32 %i1, ptr %s0, !dbg !45
  %i3 = load i32, ptr %s0, !dbg !46
  %i4.c = icmp slt i32 0, %i3, !dbg !46
  br i1 %i4.c, label %bb1, label %bb2, !dbg !46
bb1:
  %i5 = load i32, ptr %s0, !dbg !47
  store i32 %i5, ptr %s1, !dbg !48
  %i7 = load i32, ptr %s1, !dbg !49
  store i32 %i7, ptr %s2, !dbg !50
  br label %bb2
bb2:
  ret i32 0, !dbg !51
}

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!52, !53}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!3 = !DISubroutineType(types: !{!2, !2, !2})
!4 = distinct !DISubprogram(name: "scale", linkageName: "ll_debug_scopes:main.scale", scope: !0, file: !0, line: 7, type: !3, scopeLine: 7, spFlags: DISPFlagDefinition, unit: !1)
!5 = !DILexicalBlock(scope: !4, file: !0, line: 9, column: 5)
!6 = !DILexicalBlock(scope: !5, file: !0, line: 10, column: 9)
!7 = !DILexicalBlock(scope: !6, file: !0, line: 12, column: 22)
!8 = !DILocalVariable(name: "a", arg: 1, scope: !4, file: !0, line: 7, type: !2)
!9 = !DILocation(line: 7, column: 10, scope: !4)
!10 = !DILocalVariable(name: "b", arg: 2, scope: !4, file: !0, line: 7, type: !2)
!11 = !DILocation(line: 7, column: 18, scope: !4)
!12 = !DILocalVariable(name: "s", scope: !4, file: !0, line: 8, type: !2)
!13 = !DILocation(line: 8, column: 9, scope: !4)
!14 = !DILocalVariable(name: "s", scope: !6, file: !0, line: 11, type: !2)
!15 = !DILocation(line: 11, column: 17, scope: !6)
!16 = !DILocalVariable(name: "t", scope: !7, file: !0, line: 13, type: !2)
!17 = !DILocation(line: 13, column: 21, scope: !7)
!18 = !DILocalVariable(name: "u", scope: !5, file: !0, line: 17, type: !2)
!19 = !DILocation(line: 17, column: 13, scope: !5)
!20 = !DILocation(line: 8, column: 13, scope: !4)
!21 = !DILocation(line: 8, column: 17, scope: !4)
!22 = !DILocation(line: 8, column: 5, scope: !4)
!23 = !DILocation(line: 11, column: 21, scope: !6)
!24 = !DILocation(line: 11, column: 13, scope: !6)
!25 = !DILocation(line: 12, column: 16, scope: !6)
!26 = !DILocation(line: 13, column: 25, scope: !7)
!27 = !DILocation(line: 13, column: 17, scope: !7)
!28 = !DILocation(line: 14, column: 24, scope: !7)
!29 = !DILocation(line: 14, column: 17, scope: !7)
!30 = !DILocation(line: 17, column: 9, scope: !5)
!31 = !DILocation(line: 18, column: 16, scope: !5)
!32 = !DILocation(line: 18, column: 20, scope: !5)
!33 = !DILocation(line: 18, column: 9, scope: !5)
!34 = !DISubroutineType(types: !{!2})
!35 = distinct !DISubprogram(name: "main", linkageName: "ll_debug_scopes:main.main", scope: !0, file: !0, line: 22, type: !34, scopeLine: 22, spFlags: DISPFlagDefinition, unit: !1)
!36 = !DILexicalBlock(scope: !35, file: !0, line: 26, column: 9)
!37 = !DILocalVariable(name: "x", scope: !35, file: !0, line: 24, type: !2)
!38 = !DILocation(line: 24, column: 9, scope: !35)
!39 = !DILocalVariable(name: "y", scope: !36, file: !0, line: 27, type: !2)
!40 = !DILocation(line: 27, column: 17, scope: !36)
!41 = !DILocalVariable(name: "z", scope: !36, file: !0, line: 28, type: !2)
!42 = !DILocation(line: 28, column: 17, scope: !36)
!43 = !DILocation(line: 23, column: 5, scope: !35)
!44 = !DILocation(line: 24, column: 13, scope: !35)
!45 = !DILocation(line: 24, column: 5, scope: !35)
!46 = !DILocation(line: 25, column: 8, scope: !35)
!47 = !DILocation(line: 27, column: 21, scope: !36)
!48 = !DILocation(line: 27, column: 13, scope: !36)
!49 = !DILocation(line: 28, column: 21, scope: !36)
!50 = !DILocation(line: 28, column: 13, scope: !36)
!51 = !DILocation(line: 31, column: 5, scope: !35)
!52 = !{i32 7, !"Dwarf Version", i32 4}
!53 = !{i32 2, !"Debug Info Version", i32 3}
