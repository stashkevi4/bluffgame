const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function translations(saved = '"uk"') {
  const context = {localStorage:{getItem:()=>saved}, navigator:{language:'en'}};
  vm.runInNewContext(fs.readFileSync('web/i18n.js','utf8') + ';this.i18n={t,html,translateMarkup,setLanguage:value=>language=value};',context);
  return context.i18n;
}

test('static interface text translates without changing interpolated quiz content', () => {
  const {html} = translations();
  const userAnswer = 'Correct answer';
  assert.equal(html`<h2>Correct answer</h2><p>${userAnswer}</p>`, '<h2>Правильна відповідь</h2><p>Correct answer</p>');
});
test('switching back to English restores source text and keeps names intact', () => {
  const i18n = translations();
  assert.equal(i18n.t('{name} earns +1',{name:'Truth <&> Оля'}),'Truth <&> Оля отримує +1');
  i18n.setLanguage('en');
  assert.equal(i18n.t('The host’s lie'),'The host’s lie');
  assert.equal(i18n.html`<label>Your name</label>`, '<label>Your name</label>');
});
test('form accessibility labels, placeholders and server validation errors translate', () => {
  const {translateMarkup,t} = translations();
  assert.equal(translateMarkup('<input placeholder="Your name" aria-label="Room code">'), '<input placeholder="Ваше ім’я" aria-label="Код кімнати">');
  assert.equal(t('Question 2: the host lie must differ from the correct answer'),'Запитання 2: вигадка ведучого має відрізнятися від правильної відповіді');
  assert.equal(t('This player\'s result is already revealed'),'Результат цього гравця вже розкрито');
});

function gameUI() {
  const elements = new Map();
  const element = selector => {
    if (!elements.has(selector)) elements.set(selector,{innerHTML:'',textContent:'',addEventListener(){},setAttribute(){},querySelectorAll(){return []}});
    return elements.get(selector);
  };
  const context = {
    localStorage:{getItem:()=>null}, navigator:{language:'en'},
    document:{querySelector:element,querySelectorAll:()=>[],documentElement:{},addEventListener(){}},
    window:{addEventListener(){}},setInterval(){},setTimeout(){},clearTimeout(){},structuredClone,
    fetch:()=>new Promise(()=>{})
  };
  vm.runInNewContext(fs.readFileSync('web/i18n.js','utf8')+fs.readFileSync('web/app.js','utf8')+`;
    renderRoom=()=>{};
    this.ui={render:value=>{room=value;return revealPanel()},update:(before,after)=>{room=before;setRoom(after)},starter:lang=>{language=lang;return starterQuestions()},library:items=>{quizzes=items;library();return app.innerHTML}};`,context);
  return {ui:context.ui,elements};
}
function revealSnapshot(me, result) {
  return {code:'ABCDE',questionNumber:1,me,isHost:!me,revealComplete:false,
    serverTime:new Date().toISOString(),players:[{id:'alice',name:'Alice'},{id:'bob',name:'Bob'}],results:[result]};
}
test('correct answers never show a phantom author bonus to the host', () => {
  const {ui,elements} = gameUI();
  const result = {playerId:'alice',kind:'correct',points:2,text:'Truth'};
  const after = revealSnapshot(undefined,result), before = {...after,results:[]};
  assert.doesNotMatch(ui.render(after),/personal-gain/);
  ui.update(before,after);
  assert.equal(elements.get('#toast')?.textContent ?? '','');
});
test('only the actual bluff author sees the personal bonus message', () => {
  const {ui,elements} = gameUI();
  const result = {playerId:'bob',kind:'player',points:0,text:'Bluff',authorId:'alice',authorPoints:1};
  const after = revealSnapshot('alice',result);
  assert.match(ui.render(after),/Your bluff fooled Bob\. \+1 for you!/);
  ui.update({...after,results:[]},after);
  assert.equal(elements.get('#toast').textContent,'Your bluff fooled Bob. +1 for you!');
  assert.doesNotMatch(ui.render(revealSnapshot('bob',result)),/personal-gain/);
});
test('starter quizzes follow the selected language without mixing answer languages', () => {
  const {ui} = gameUI();
  const uk = ui.starter('uk');
  assert.equal(uk.length,10);
  assert.equal(uk[1].correct,'Аглет');
  const en = ui.starter('en');
  assert.equal(en[1].correct,'Aglet');
  assert.equal(en[1].prompt,'What is the tiny plastic tip on a shoelace called?');
  en[1].correct='Edited';
  assert.equal(ui.starter('en')[1].correct,'Aglet');
});
test('shared games offer copies while personal quizzes offer editing', () => {
  const {ui} = gameUI();
  const output=ui.library([{id:'builtin-test',title:'Ready-made',builtin:true,language:'uk'},{id:'personal-test',title:'My quiz'}]);
  assert.match(output,/Ready-made games/);
  assert.match(output,/УКР · Built-in/);
  assert.match(output,/data-copy-quiz="builtin-test"/);
  assert.doesNotMatch(output,/href="#editor\/builtin-test"/);
  assert.match(output,/href="#editor\/personal-test"/);
});
