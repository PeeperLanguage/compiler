source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [8 x i8] c"scaled\0A\00"

define i32 @"ll_debug_scopes:main.scale"(i32 %arg0, i32 %arg1) !dbg !4 {
entry:
  %v0 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !8, metadata !DIExpression()), !dbg !9
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !10, metadata !DIExpression()), !dbg !11
  %v2 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v2, metadata !12, metadata !DIExpression()), !dbg !13
  %v3 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v3, metadata !14, metadata !DIExpression()), !dbg !15
  %v4 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v4, metadata !16, metadata !DIExpression()), !dbg !17
  %v5 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v5, metadata !18, metadata !DIExpression()), !dbg !19
  br label %bb0
bb0:
  store i32 %arg0, ptr %v0, !dbg !9
  store i32 %arg1, ptr %v1, !dbg !11
  %i2 = load i32, ptr %v0, !dbg !20
  %i3 = load i32, ptr %v1, !dbg !21
  %i4 = add i32 %i2, %i3, !dbg !20
  store i32 %i4, ptr %v2, !dbg !22
  %i6 = load i32, ptr %v2, !dbg !23
  %i7 = mul i32 %i6, 2, !dbg !23
  store i32 %i7, ptr %v3, !dbg !24
  %i9 = load i32, ptr %v3, !dbg !25
  %i10 = icmp sgt i32 %i9, 2, !dbg !25
  br i1 %i10, label %bb1, label %bb2, !dbg !25
bb1:
  %i11 = load i32, ptr %v3, !dbg !26
  %i12 = sdiv i32 %i11, 2, !dbg !26
  store i32 %i12, ptr %v4, !dbg !27
  %i14 = load i32, ptr %v4, !dbg !28
  ret i32 %i14, !dbg !29
bb2:
  store i32 3, ptr %v5, !dbg !30
  %i16 = load i32, ptr %v5, !dbg !31
  %i17 = load i32, ptr %v2, !dbg !32
  %i18 = add i32 %i16, %i17, !dbg !31
  ret i32 %i18, !dbg !33
bb3:
  br label %bb2
bb4:
  unreachable
}

define i32 @"ll_debug_scopes:main.main"() !dbg !35 {
entry:
  %v0 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !38, metadata !DIExpression()), !dbg !39
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !40, metadata !DIExpression()), !dbg !41
  %v2 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v2, metadata !42, metadata !DIExpression()), !dbg !43
  br label %bb0
bb0:
  %i0 = call i32 @"peeper_rt_v1_write"(i32 1, ptr @.str.0, i32 7), !dbg !44
  %i1 = call i32 @"ll_debug_scopes:main.scale"(i32 1, i32 2), !dbg !45
  store i32 %i1, ptr %v0, !dbg !46
  %i3 = load i32, ptr %v0, !dbg !47
  %i4 = icmp sgt i32 %i3, 0, !dbg !47
  br i1 %i4, label %bb1, label %bb2, !dbg !47
bb1:
  %i5 = load i32, ptr %v0, !dbg !48
  store i32 %i5, ptr %v1, !dbg !49
  %i7 = load i32, ptr %v1, !dbg !50
  store i32 %i7, ptr %v2, !dbg !51
  br label %bb2
bb2:
  ret i32 0, !dbg !52
bb3:
  unreachable
}

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!53, !54}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!3 = !DISubroutineType(types: !{!2, !2, !2})
!4 = distinct !DISubprogram(name: "scale", linkageName: "ll_debug_scopes:main.scale", scope: !0, file: !0, line: 5, type: !3, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!5 = !DILexicalBlock(scope: !4, file: !0, line: 7, column: 5)
!6 = !DILexicalBlock(scope: !5, file: !0, line: 8, column: 9)
!7 = !DILexicalBlock(scope: !6, file: !0, line: 10, column: 22)
!8 = !DILocalVariable(name: "a", arg: 1, scope: !4, file: !0, line: 5, type: !2)
!9 = !DILocation(line: 5, column: 10, scope: !4)
!10 = !DILocalVariable(name: "b", arg: 2, scope: !4, file: !0, line: 5, type: !2)
!11 = !DILocation(line: 5, column: 18, scope: !4)
!12 = !DILocalVariable(name: "s", scope: !4, file: !0, line: 6, type: !2)
!13 = !DILocation(line: 6, column: 9, scope: !4)
!14 = !DILocalVariable(name: "s", scope: !6, file: !0, line: 9, type: !2)
!15 = !DILocation(line: 9, column: 17, scope: !6)
!16 = !DILocalVariable(name: "t", scope: !7, file: !0, line: 11, type: !2)
!17 = !DILocation(line: 11, column: 21, scope: !7)
!18 = !DILocalVariable(name: "u", scope: !5, file: !0, line: 15, type: !2)
!19 = !DILocation(line: 15, column: 13, scope: !5)
!20 = !DILocation(line: 6, column: 13, scope: !4)
!21 = !DILocation(line: 6, column: 17, scope: !4)
!22 = !DILocation(line: 6, column: 5, scope: !4)
!23 = !DILocation(line: 9, column: 21, scope: !6)
!24 = !DILocation(line: 9, column: 13, scope: !6)
!25 = !DILocation(line: 10, column: 16, scope: !6)
!26 = !DILocation(line: 11, column: 25, scope: !7)
!27 = !DILocation(line: 11, column: 17, scope: !7)
!28 = !DILocation(line: 12, column: 24, scope: !7)
!29 = !DILocation(line: 12, column: 17, scope: !7)
!30 = !DILocation(line: 15, column: 9, scope: !5)
!31 = !DILocation(line: 16, column: 16, scope: !5)
!32 = !DILocation(line: 16, column: 20, scope: !5)
!33 = !DILocation(line: 16, column: 9, scope: !5)
!34 = !DISubroutineType(types: !{!2})
!35 = distinct !DISubprogram(name: "main", linkageName: "ll_debug_scopes:main.main", scope: !0, file: !0, line: 20, type: !34, scopeLine: 20, spFlags: DISPFlagDefinition, unit: !1)
!36 = !DILexicalBlock(scope: !35, file: !0, line: 20, column: 18)
!37 = !DILexicalBlock(scope: !36, file: !0, line: 24, column: 9)
!38 = !DILocalVariable(name: "x", scope: !36, file: !0, line: 22, type: !2)
!39 = !DILocation(line: 22, column: 9, scope: !36)
!40 = !DILocalVariable(name: "y", scope: !37, file: !0, line: 25, type: !2)
!41 = !DILocation(line: 25, column: 17, scope: !37)
!42 = !DILocalVariable(name: "z", scope: !37, file: !0, line: 26, type: !2)
!43 = !DILocation(line: 26, column: 17, scope: !37)
!44 = !DILocation(line: 21, column: 5, scope: !36)
!45 = !DILocation(line: 22, column: 13, scope: !36)
!46 = !DILocation(line: 22, column: 5, scope: !36)
!47 = !DILocation(line: 23, column: 8, scope: !36)
!48 = !DILocation(line: 25, column: 21, scope: !37)
!49 = !DILocation(line: 25, column: 13, scope: !37)
!50 = !DILocation(line: 26, column: 21, scope: !37)
!51 = !DILocation(line: 26, column: 13, scope: !37)
!52 = !DILocation(line: 29, column: 5, scope: !36)
!53 = !{i32 7, !"Dwarf Version", i32 4}
!54 = !{i32 2, !"Debug Info Version", i32 3}
