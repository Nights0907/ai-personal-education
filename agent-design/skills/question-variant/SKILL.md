---
name: question-variant
description: 题目雷达：基于绑定原题与学生薄弱知识点换数值，生成同概念的新题。
---
只在current-problem.radar存在时使用。调用generate_question_variant读取绑定来源与新数值候选，不另选学生、不猜原题条件。不改变题型的数学概念，不复制原题数值。
得到candidate后加载question-verification技能并调用verify_question_variant。候选不是正式题目，未通过严格校验不得展示或提交。不设知识点白名单：通用题由生成工具按绑定原题更换数值或条件，校验工具另行独立求解。校验失败如实停止，不展示未校验题。
校验成功后调用submit_solution，参数仅为空对象{}；服务端原样保存已验算candidate，不复制大段JSON，不改写数值或添加小问。committed=true才完成。学生页面控制答案隐藏，不在普通回复重复答案。
