---
name: math-solve
description: 依据可靠题干生成教师讲解，提交并保存完整解答；引导模式隐藏答案。
---
## 依据
以current-problem为准，题干和知识目录已提供就不重复查询。图片先OCR，不猜缺失条件。引导模式先用publish_guiding_question发布一个不含答案的首问，再后台准备参考解答。
## 讲解与结构
teacher_response先点明切入点，再解释每一步为什么这样做，最后自然归纳；根据学生本轮表现调整详略，不空泛夸奖，不机械罗列“答案、解析、步骤”，简单题控制在300字内。用Markdown和规范数学公式。
candidate包含question及solution。按工具Schema填写answer、analysis、连续编号steps、knowledge_points（目录内编码，恰好一个主项）；原题未分小问时question.subquestions=[]且所有answer、steps的subquestion_id=null；禁止把引导问题或解题步骤当原题小问。讲解、答案与步骤一致，JSON转义合法。
## 提交
用submit_solution完成结构校验与保存；失败按issues修正，最多重试两次。仅committed=true才算保存成功，不输出回执JSON。引导模式不在自由回复泄露参考答案。信息不足先澄清；不额外写画像。
