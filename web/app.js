'use strict';

const $ = (selector) => document.querySelector(selector);
const app = $('#app');
const esc = (value) => String(value ?? '').replace(/[&<>\x22\x27]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const store = {
  get(key, fallback = null) { try { return JSON.parse(localStorage.getItem(`options:${key}`)) ?? fallback; } catch { return fallback; } },
  set(key, value) { try { localStorage.setItem(`options:${key}`, JSON.stringify(value)); } catch { /* The game still works for the current page. */ } },
  remove(key) { try { localStorage.removeItem(`options:${key}`); } catch { /* Unavailable storage. */ } }
};
let quizzes = [], draft = null, questionIndex = 0, room = null, roomToken = '', roomRole = '', route = '', polling = null, clockOffset = 0, busy = false, toastTimeout, lastRoomSignature = '', pollFailures = 0, loadingRoom = false;

async function api(path, method = 'GET', body, auth = '') {
  const headers = {'Content-Type':'application/json'};
  if (auth) headers.Authorization = `Bearer ${auth}`;
  const res = await fetch(`/api${path}`, {method, headers, credentials:'same-origin', body: body === undefined ? undefined : JSON.stringify(body)});
  let result;
  try { result = await res.json(); } catch { throw new Error(t('The server could not be reached. Please try again.')); }
  if (!res.ok) throw new Error(result.error || t('Something went wrong. Please try again.'));
  return result;
}
function toast(message) { $('#toast').textContent = t(message); $('#toast').hidden = false; clearTimeout(toastTimeout); toastTimeout = setTimeout(() => $('#toast').hidden = true, 5000); }
function goto(hash) { if (location.hash === `#${hash}`) navigate(); else location.hash = hash; }
function preserveRender(html) {
  const fields = [...app.querySelectorAll('input,textarea')].map(el => ({id:el.id,value:el.value}));
  const active = document.activeElement, activeID = active?.id;
  const start = active?.selectionStart, end = active?.selectionEnd;
  app.innerHTML = html;
  for (const field of fields) { const el = document.getElementById(field.id); if (el && el.type !== 'checkbox') el.value = field.value; }
  const focused = activeID && document.getElementById(activeID);
  if (focused && !focused.disabled) { focused.focus({preventScroll:true}); if (typeof start === 'number') focused.setSelectionRange(start,end); }
}
const demoQuestions = [
  ['Which animal has three hearts?', 'Octopus', 'Flamingo'],
  ['What is the tiny plastic tip on a shoelace called?', 'Aglet', 'Lacet'],
  ['What is the only mammal capable of sustained flight?', 'Bat', 'Flying squirrel'],
  ['Which planet has the shortest day?', 'Jupiter', 'Mercury'],
  ['What was the first product to have its barcode scanned at a checkout?', 'Chewing gum', 'A carton of milk'],
  ['Which country has a unicorn as its national animal?', 'Scotland', 'Iceland'],
  ['What is the dot over a lowercase i or j called?', 'Tittle', 'Pip'],
  ['What is the largest organ of the human body?', 'Skin', 'Liver'],
  ['In which country was the modern paper clip patented by William Middlebrook?', 'United States', 'Norway'],
  ['What is a group of crows traditionally called?', 'A murder', 'A conspiracy']
].map(([prompt,correct,lie]) => ({prompt,correct,lie}));

function resumeBanner() {
  const last = store.get('lastRoom');
  if (!last) return '';
  return html`<div class="resume-banner"><span>There’s a seat waiting in room <b>${esc(last.code)}</b>.</span><a class="btn small secondary" href="#room/${esc(last.code)}/${esc(last.role)}">Back to game ↗</a></div>`;
}
function joinForm(code = '') { return html`<form id="join-form"><div class="input-row"><label class="sr-only" for="join-code">Room code</label><input id="join-code" name="code" placeholder="CODE" maxlength="5" minlength="5" autocomplete="off" autocapitalize="characters" required value="${esc(code)}"><label class="sr-only" for="join-name">Your name</label><input id="join-name" name="name" placeholder="Your name" maxlength="24" autocomplete="nickname" required value="${esc(store.get('name',''))}"><button class="btn dark" type="submit">Join <span aria-hidden="true">↗</span></button></div></form>`; }
function home() {
  app.innerHTML = html`${resumeBanner()}<section class="hero"><div class="hero-copy"><span class="eyebrow"><span class="dot"></span> THE BELIEVABLE BLUFFING GAME</span><h1>Good friends.<br>Great <em>liars.</em></h1><p>One real answer. A room full of convincing lies.<br>How well can you fool your friends?</p><div class="small-facts"><span><span class="fact-icon">♧</span> 1 host + your crew</span><span><span class="fact-icon">◷</span> 10 questions</span><span><span class="fact-icon">▯</span> Play on your phone</span></div></div><div class="hero-art" aria-label="One truth, many possibilities"><div class="orbit"></div><div class="float-card card-back"><div class="card-kicker"><span>THE TRUTH</span><span>✳</span></div><p>Stranger than<br>you think.</p></div><div class="float-card card-front"><div class="card-kicker"><span>A VERY GOOD LIE</span><span>↗</span></div><p>Sounds about<br>right. Right?</p><div class="card-bottom"><span>Confidence is everything.</span><b>?</b></div></div><span class="art-star" aria-hidden="true">✳</span><span class="art-caption">Trust no one. Have fun.</span></div></section><section class="play-cards" aria-label="Choose how to play"><div class="play-card host"><div class="card-heading"><span class="role-icon" aria-hidden="true">✳</span><span class="tag">For the ringleader</span></div><h3>Bring the questions.</h3><p>Make a 10-question quiz, add your best lies, and get the room together. You’re in charge.</p><a class="btn full" href="#library">Host a game <span aria-hidden="true">↗</span></a>${quizzes.length ? html`<div class="home-library">Already prepared? <a href="#library">${t(quizzes.length === 1 ? 'Open your quiz' : 'Open your quizzes')}</a></div>` : ''}</div><div class="play-card join"><div class="card-heading"><span class="role-icon" aria-hidden="true">↗</span><span class="tag">For the smooth talkers</span></div><h3>Bring your poker face.</h3><p>Get the room code from your host, pick a name, and prepare to be very convincing.</p>${joinForm()}</div></section><div class="steps-strip"><b>01 &nbsp; Write a bluff</b><span class="separator">→</span><b>02 &nbsp; Find the truth</b><span class="separator">→</span><b>03 &nbsp; Take the glory</b></div>`;
  bindJoin();
}
function bindJoin() {
  $('#join-form')?.addEventListener('submit', e => run(e, async () => {
    const code = $('#join-code').value.trim().toUpperCase(), name = $('#join-name').value.trim();
    let saved = store.get(`player:${code}`);
    if (saved) {
      try { await api(`/sessions/${code}`, 'GET', undefined, saved); store.set('lastRoom',{code,role:'play'}); goto(`room/${code}/play`); return; } catch { /* Allow rejoining if the stored seat no longer exists. */ }
    }
    const joined = await api('/join','POST',{code,name});
    store.set(`player:${joined.code}`,joined.token); store.set('name',name); store.set('lastRoom',{code:joined.code,role:'play'});
    roomToken = joined.token;
    goto(`room/${joined.code}/play`);
  }));
}
async function run(event, fn) {
  event?.preventDefault();
  if (busy) return;
  busy = true;
  const button = event?.submitter || (event?.currentTarget?.tagName === 'BUTTON' ? event.currentTarget : null);
  if (button) button.disabled = true;
  try { await fn(); } catch (error) { toast(error.message || t('Connection lost. Please try again.')); }
  finally { busy = false; if (button?.isConnected) button.disabled = false; }
}
function quizCard(q, index) {
  return html`<article class="quiz-card"><div class="card-heading"><span class="role-icon" aria-hidden="true">${['✳','◇','◎'][index%3]}</span><span class="tag">${q.builtin ? `${q.language==='uk' ? 'УКР' : 'EN'} · ${t('Built-in')}` : t('10 questions')}</span></div><h3>${esc(q.title)}</h3><p class="muted">${q.builtin ? t('10 surprising questions. Ready to play.') : t('Your questions. Your best lies.')}</p><div class="actions"><button class="btn small" data-host="${q.id}">Start a room ↗</button>${q.builtin ? html`<button class="btn small secondary" data-copy-quiz="${q.id}">Make a copy</button>` : html`<a href="#editor/${q.id}" class="btn small secondary">Edit</a>`}</div></article>`;
}
function library() {
  const personal = quizzes.filter(q=>!q.builtin);
  const builtins = quizzes.filter(q=>q.builtin).sort((a,b)=>Number(a.language!==language)-Number(b.language!==language));
  app.innerHTML = html`<div class="page-heading"><div><span class="eyebrow">YOUR COLLECTION</span><h2>A good night starts here.</h2><p>Make it weird. Make it believable. Play it as many times as you like.</p></div><a href="#editor/new" class="btn">+ &nbsp; Create a quiz</a></div>
    ${personal.length ? html`<h3 class="collection-heading">Your own quizzes</h3><section class="quiz-grid">${personal.map(quizCard).join('')}</section>` : ''}
    ${builtins.length ? html`<div class="collection-intro"><h3>Ready-made games</h3><p class="muted">8 Ukrainian · 2 English · 10 questions each</p><p class="muted">Available to every host. Make a copy to customize.</p></div><section class="quiz-grid">${builtins.map(quizCard).join('')}</section>` : personal.length ? '' : html`<section class="empty-state"><div class="empty-icon">✳</div><h3>Your next game night is a blank canvas.</h3><p class="muted">Create your first quiz, or start with 10 ready-to-play questions and make them your own.</p><div class="button-row"><a href="#editor/new" class="btn">Create a quiz ↗</a><button id="starter" class="btn secondary">Use the starter quiz</button></div></section>`}
    <p class="muted save-status">Your quizzes are saved on this server and linked to this browser. Keep its cookies to keep host access.</p>`;
  bindHost();
  $$('[data-copy-quiz]').forEach(el=>el.addEventListener('click',()=>{
    const original=quizzes.find(q=>q.id===el.dataset.copyQuiz);
    draft={title:original.title,language:original.language,questions:structuredClone(original.questions)};
    store.set('draft:new',draft);
    goto('editor/new');
  }));
  $('#starter')?.addEventListener('click', startDemo);
}
function startDemo() { draft = {title:t('Curiously convincing'),questions:starterQuestions()}; store.set('draft:new',draft); goto('editor/new'); }
function newDraft() { return {title:'',questions:Array.from({length:10}, () => ({prompt:'',correct:'',lie:''}))}; }
function complete(q) { return !!(q.prompt.trim() && q.correct.trim() && q.lie.trim() && q.correct.trim().toLowerCase() !== q.lie.trim().toLowerCase()); }
function draftKey() { return `draft:${route.split('/')[1] || 'new'}`; }
function saveDraft() {
  if (!draft) return;
  draft.title = $('#quiz-title').value;
  draft.questions[questionIndex] = {prompt:$('#question-prompt').value,correct:$('#correct-answer').value,lie:$('#host-lie').value};
  store.set(draftKey(),draft);
  updateEditorProgress();
}
function updateEditorProgress() {
  const count = draft.questions.filter(complete).length;
  $('#complete-count').textContent = html`${count} of 10 ready`;
  $$('.question-dot').forEach((el,i) => el.classList.toggle('done',complete(draft.questions[i])));
  $('#save-quiz').disabled = count !== 10 || !draft.title.trim();
}
const $$ = (selector) => [...document.querySelectorAll(selector)];
function editor() {
  const q = draft.questions[questionIndex], edit = !!draft.id;
  app.innerHTML = html`<a class="back-link" href="#library">← &nbsp; Your quizzes</a><div class="page-heading"><div><span class="eyebrow">THE HOST’S NOTEBOOK</span><h2>${edit ? t('Fine-tune the fun.') : t('Set the scene.')}</h2><p>Ten little questions. Endless questionable answers.</p></div>${!edit ? translateMarkup('<button class="btn secondary small" id="fill-demo">Use starter questions ↗</button>') : ''}</div><form id="quiz-form" class="editor-layout"><aside class="editor-sidebar"><span class="eyebrow">BUILD YOUR QUIZ</span><h3>Make them wonder.</h3><p>Choose short, surprising answers. A great host lie should feel just plausible enough.</p><div class="question-dots" aria-label="Select question">${draft.questions.map((q,i) => html`<button type="button" class="question-dot ${i === questionIndex ? 'active' : ''} ${complete(q) ? 'done' : ''}" data-question="${i}" aria-label="${esc(t('Question'))} ${i+1}" ${i === questionIndex ? 'aria-current="step"' : ''}>${i+1}</button>`).join('')}</div><div class="muted" id="complete-count"></div></aside><div class="editor-main"><label class="field"><span>Quiz title</span><input id="quiz-title" class="title-input" placeholder="e.g. Things you should probably know" maxlength="80" required value="${esc(draft.title)}"></label><div class="question-title"><h3>Question ${questionIndex+1}</h3><span class="tag">${questionIndex+1} / 10</span></div><label class="field"><span>The question</span><textarea id="question-prompt" rows="3" placeholder="What is the tiny plastic tip on a shoelace called?" maxlength="300" required>${esc(q.prompt)}</textarea></label><div class="answer-fields"><label class="field"><span class="correct-label">● &nbsp; The correct answer</span><input id="correct-answer" placeholder="Aglet" maxlength="100" required value="${esc(q.correct)}"><small>The official truth. Finding this option earns +2.</small></label><label class="field"><span class="lie-label">◇ &nbsp; Your convincing lie</span><input id="host-lie" placeholder="Lacet" maxlength="100" required value="${esc(q.lie)}"><small>Your trap. Picking this option costs −1.</small></label></div><div class="editor-actions"><button type="button" class="btn secondary" id="previous-question" ${questionIndex===0 ? 'disabled' : ''}>← Previous</button>${questionIndex<9 ? translateMarkup('<button type="button" class="btn secondary" id="next-question">Next question →</button>') : ''}<button type="submit" class="btn" id="save-quiz">Save quiz ↗</button></div><p class="save-status">Draft saved on this device as you type. All 10 questions are required.</p></div></form>`;
  updateEditorProgress();
  $('#quiz-form').addEventListener('input',saveDraft);
  $$('.question-dot').forEach(el => el.addEventListener('click', () => selectQuestion(Number(el.dataset.question))));
  $('#previous-question').addEventListener('click', () => selectQuestion(questionIndex-1));
  $('#next-question')?.addEventListener('click', () => selectQuestion(questionIndex+1));
  $('#fill-demo')?.addEventListener('click', () => { saveDraft(); draft = {title:draft.title || t('Curiously convincing'),questions:starterQuestions()}; store.set(draftKey(),draft); editor(); toast(t('Starter questions added. Make them your own.')); });
  $('#quiz-form').addEventListener('submit', e => run(e, async () => {
    saveDraft();
    const saved = await api(`/quizzes${edit ? `/${draft.id}` : ''}`,edit ? 'PUT' : 'POST',draft);
    store.remove(draftKey()); quizzes = [saved,...quizzes.filter(q => q.id!==saved.id)]; draft = null;
    toast(t('Quiz saved. Your game night is ready.')); goto('library');
  }));
}
function selectQuestion(index) { saveDraft(); questionIndex = index; editor(); $('#question-prompt').focus({preventScroll:true}); }
function bindHost() { $$('[data-host]').forEach(el => el.addEventListener('click', e => run(e, () => createRoom(el.dataset.host)))); }
async function createRoom(quizId) {
  const result = await api('/sessions','POST',{quizId});
  store.set('lastRoom',{code:result.code,role:'host'}); goto(`room/${result.code}/host`);
}
function nameOf(id) { return room.players.find(p => p.id===id)?.name || t('Player'); }
function person(p, index, final = false) {
  const delta = room.changes?.[p.id] || 0;
  return html`<li class="player-row">${final ? html`<span class="rank">${index+1}</span>` : ''}<span class="avatar" aria-hidden="true">${esc(p.name.slice(0,1).toUpperCase())}</span><span class="player-name">${esc(p.name)}${room.me===p.id ? translateMarkup('<small>(you)</small>') : ''}</span>${room.phase==='lobby' ? html`<span class="ready-pill ${p.ready ? '' : 'wait'}">${p.ready ? t('✓ Ready') : t('Getting ready')}</span>` : html`<span class="score">${p.score} <small>pts</small>${delta && room.phase==='reveal' ? html`<span class="delta ${delta<0 ? 'negative' : ''}">${delta>0 ? '+' : ''}${delta}</span>` : ''}</span>`}</li>`;
}
function roomTop() { return html`<div id="connection-state" ${pollFailures ? '' : 'hidden'} class="connection-banner">Connection interrupted. Reconnecting… the server timer keeps running.</div><div class="room-top"><span class="room-title">${esc(room.title)}<br><span class="eyebrow">${room.isHost ? t('HOST VIEW') : t('PLAYER VIEW')}</span></span><div class="room-code">ROOM CODE <button data-copy="code" aria-label="Copy room code">${esc(room.code)}</button></div></div>`; }
function lobby() {
  const ready = room.players.filter(p => p.ready).length, me = room.players.find(p => p.id===room.me);
  return html`<section class="room-card center"><div class="lobby-icon" aria-hidden="true">♧</div><span class="eyebrow">THE MORE, THE MERRIER</span><h2>${room.isHost ? t('Gather your good liars.') : t('You’re in good company.')}</h2><p class="muted intro">${room.isHost ? t('Share the room code with your friends. Everyone joins on their own phone — no account needed.') : esc(t('Welcome, {name}. Get comfortable, get ready, and let the host take it from here.', {name:me?.name}))}</p><div class="share-box"><div><p>YOUR ROOM CODE</p><strong>${esc(room.code)}</strong></div><button class="btn secondary small" data-copy="link">Copy invite ↗</button></div><div class="lobby-players"><div class="list-heading"><span>The crew · ${room.players.length}/20</span><span>${ready} ready</span></div><ul class="player-list">${room.players.length ? room.players.map((p,i) => person(p,i)).join('') : translateMarkup('<li class="waiting-note">The first arrival gets bragging rights.<br>Waiting for players to join…</li>')}</ul></div><div class="lobby-controls">${room.isHost ? html`<button class="btn full" data-action="start" ${!room.players.length || ready!==room.players.length ? 'disabled' : ''}>Start the first question <span>↗</span></button><p class="muted">${!room.players.length ? t('At least one player needs to join.') : ready!==room.players.length ? t('The game starts when everyone is ready.') : t('Everyone’s ready. Let the bluffing begin.')}</p>` : html`<button class="btn full ${me?.ready ? 'secondary' : ''}" data-action="ready">${me?.ready ? t('✓ You’re ready — tap to undo') : t('I’m ready')} <span>↗</span></button><p class="muted">${me?.ready ? t('Waiting for the host to start the game.') : t('Tap when you’re settled in.')}</p>`}</div></section>`;
}
function scoreboard() { return html`<aside class="scoreboard"><h3>The standings</h3><ul class="player-list">${room.players.map((p,i) => person(p,i)).join('')}</ul></aside>`; }
function progress(count, label) { const total = room.players.length; return html`<div class="progress-label"><label for="round-progress">${label}</label><span>${count} / ${total}</span></div><progress id="round-progress" class="round-progress" value="${count}" max="${total || 1}">${count} / ${total}</progress>`; }
function optionList(reveal = false) {
  return html`<div class="options">${room.options.map((o,i) => {
    const selected = room.myVote === o.id;
    const author = o.kind==='correct' ? t('The official truth · +2 for finding it') : o.kind==='host' ? t('The host’s lie · −1 for picking it') : t('{name}’s bluff · +1 for its author per vote', {name:nameOf(o.authorId)});
    const voters = reveal && o.voters?.length ? o.voters.map(nameOf).join(', ') : '';
    return html`<${reveal ? 'div' : 'button type="button"'} class="option ${o.isMine ? 'mine' : ''} ${selected ? 'selected' : ''} ${o.kind==='correct' ? 'correct' : ''}" ${!reveal ? `data-vote="${o.id}" ${room.isHost || room.myVote || o.isMine ? 'disabled' : ''} aria-pressed="${selected}"` : ''}><span class="option-letter">${o.kind==='correct' ? '✓' : String.fromCharCode(65+i)}</span><span class="option-content">${esc(o.text)}${reveal ? html`<small>${esc(author)}</small>${voters ? html`<small>${esc(t('Picked by {names}', {names:voters}))}</small>` : ''}` : ''}</span>${selected ? translateMarkup('<span class="option-label">Your vote ✓</span>') : o.isMine ? translateMarkup('<span class="option-label">Your bluff</span>') : ''}</${reveal ? 'div' : 'button'}>`;
  }).join('')}</div>`;
}
function revealPanel() {
  const results = room.results || [], latest = results.at(-1);
  const byPlayer = new Map(results.map(result => [result.playerId,result]));
  // Keep the picker stable as scores change, while the result feed preserves
  // the order the host chose. This avoids moving a button under their finger.
  const players = [...room.players].sort((a,b) => a.id.localeCompare(b.id));
  return html`<p class="question-helper">${room.isHost ? t('Choose a player to reveal their vote. Points are awarded only when you click.') : t('Voting is over. The host will reveal each player’s choice, one at a time.')}</p>
    ${latest ? html`<section class="latest-result" aria-live="polite" role="status"><span class="eyebrow">Latest reveal</span>${resultCard(latest)}${room.me && latest.authorId===room.me ? html`<p class="personal-gain">${esc(t('Your bluff fooled {name}. +1 for you!', {name:nameOf(latest.playerId)}))}</p>` : ''}</section>` : ''}
    <div class="reveal-players">${players.map(p => {
      const revealed = byPlayer.has(p.id);
      return html`<button type="button" class="reveal-player ${revealed ? 'is-revealed' : ''}" data-reveal="${p.id}" ${!room.isHost || revealed ? 'disabled' : ''}><span class="avatar" aria-hidden="true">${esc(p.name.slice(0,1).toUpperCase())}</span><span class="reveal-player-name">${esc(p.name)}</span><span class="reveal-player-state">${revealed ? t('Revealed')+' ✓' : room.isHost ? t('Reveal')+' ↗' : t('Waiting for the host')}</span></button>`;
    }).join('')}</div>
    ${progress(results.length,t('Results revealed'))}
    ${results.length>1 ? html`<div class="reveal-history">${results.slice(0,-1).reverse().map(result => resultCard(result)).join('')}</div>` : !results.length ? html`<p class="waiting-note">No results revealed yet.</p>` : ''}
    ${room.revealComplete ? html`<p class="truth-summary">${esc(t('The truth is {answer}.', {answer:room.correct}))}</p>${optionList(true)}` : ''}
    ${room.isHost ? html`<button class="btn next-button" data-action="next" ${room.revealComplete ? '' : 'disabled'}>${room.questionNumber===10 ? t('See the final results') : t('Start the next question')} <span>↗</span></button>${!room.revealComplete ? html`<p class="waiting-note">Reveal every player to continue.</p>` : ''}` : room.revealComplete ? html`<p class="waiting-note">${room.questionNumber===10 ? t('Waiting for the host to reveal the final results.') : t('Take a breath. The host will start the next question.')}</p>` : ''}`;
}
function resultCard(result) {
  const symbol = {correct:'✓',host:'😈',player:'🎭',none:'—'}[result.kind];
  const label = {correct:t('Correct answer'),host:t('The host’s lie'),player:t('Another player’s bluff'),none:t('No vote')}[result.kind];
  return html`<article class="reveal-result result-${result.kind}"><span class="result-symbol" aria-hidden="true">${symbol}</span><div class="result-details"><strong>${esc(nameOf(result.playerId))}</strong>${result.text ? html`<p class="result-answer">“${esc(result.text)}”</p>` : ''}<small>${label}</small>${result.authorId ? html`<p class="author-gain">${esc(t('{name} earns +1',{name:nameOf(result.authorId)}))}</p>` : ''}</div><span class="result-points">${result.points>0 ? '+' : ''}${result.points}</span></article>`;
}

function starterQuestions() {
  if (language !== 'uk') return structuredClone(demoQuestions);
  return [
    ['Яка тварина має три серця?', 'Восьминіг', 'Фламінго'],
    ['Як називається пластиковий кінчик шнурка?', 'Аглет', 'Лацет'],
    ['Який ссавець здатний до активного польоту?', 'Кажан', 'Білка-летяга'],
    ['На якій планеті найкоротша доба?', 'Юпітер', 'Меркурій'],
    ['Який товар першим просканували за штрихкодом на касі?', 'Жувальна гумка', 'Пакет молока'],
    ['Національною твариною якої країни є єдиноріг?', 'Шотландія', 'Ісландія'],
    ['Як називається крапка над латинськими літерами i та j англійською?', 'Tittle', 'Pip'],
    ['Який орган людського тіла найбільший?', 'Шкіра', 'Печінка'],
    ['У якій країні Вільям Міддлбрук запатентував сучасну скріпку?', 'США', 'Норвегія'],
    ['Як традиційно називають зграю ворон англійською?', 'A murder', 'A conspiracy']
  ].map(([prompt,correct,lie]) => ({prompt,correct,lie}));
}

const shellText = new Map($$('[data-i18n-shell]').map(el => [el,el.innerHTML]));
function applyLanguage() {
  document.documentElement.lang = language;
  $('.brand').setAttribute('aria-label',t('Options home'));
  $('.site-header nav').setAttribute('aria-label',t('Main navigation'));
  $('#language').value = language;
  for (const [el,original] of shellText) el.innerHTML = translateMarkup(original);
  document.title = t(route.startsWith('room/') ? 'Game room · Options' : 'Options — The believable bluffing game');
}
$('#language').addEventListener('change', () => {
  if (route.startsWith('editor/')) saveDraft();
  const values = [...app.querySelectorAll('input,textarea')].map(el => [el.id,el.value]);
  language = $('#language').value;
  store.set('language',language);
  applyLanguage();
  const page = route.split('/')[0];
  if (page==='editor') editor();
  else if (page==='room' && room) renderRoom();
  else if (page==='library') library();
  else if (page==='home') home();
  else if (page==='join') { app.innerHTML=html`<section class="join-page room-card"><span class="eyebrow">YOUR SEAT IS WAITING</span><h2>Bring your poker face.</h2><p class="muted">Enter a name and join the crew.</p>${joinForm(route.split('/')[1] || '')}</section>`;bindJoin(); }
  for (const [id,value] of values) { const el=document.getElementById(id);if (el) el.value=value; }
});
function live() {
  const phase = room.phase, host = room.isHost, reveal = phase==='reveal';
  let content = '';
  if (phase==='writing') {
    content = html`<p class="question-helper">${host ? t('Let the players work their magic. Answers stay hidden until the timer runs out.') : t('Know the answer? Write it. Don’t know? Make up something your friends might believe.')}</p>${host ? translateMarkup('<div class="host-note">You set the question. Now sit back and watch the bluffs roll in.<br>Voting opens automatically after 30 seconds.</div>') : room.myAnswer ? html`<div class="locked-box"><span class="check">✓</span><h3>Your answer is locked in.</h3><p>“${esc(room.myAnswer)}”</p><p>Keep your poker face. Voting opens when time is up.</p></div>` : translateMarkup('<form id="answer-form" class="answer-form"><label class="sr-only" for="player-answer">Your answer</label><textarea id="player-answer" maxlength="100" rows="3" placeholder="Make it sound like you know…" required></textarea><button class="btn full" type="submit">Lock in my answer <span>↗</span></button></form>')}${progress(room.answerCount,t('Answers locked in'))}`;
  } else if (phase==='voting') {
    content = html`<p class="question-helper">${host ? t('The truth is hiding in plain sight. Players are choosing their answer.') : room.myVote ? t('Your vote is locked in. The host will reveal results after voting ends.') : t('One of these is the official truth. Pick wisely — even identical answers can have different authors.')}</p>${!host && !room.myAnswer ? translateMarkup('<p class="inline-error">You missed the answer timer. You can still vote and earn points.</p>') : ''}${optionList()}${progress(room.voteCount,t('Votes locked in'))}`;
  } else {
    content = revealPanel();
  }
  return html`<div class="live-layout"><section class="room-card"><div class="phase-top"><span class="phase-pill">${phase==='writing' ? t('01 · Write a bluff') : phase==='voting' ? t('02 · Find the truth') : t('03 · The reveal')}</span>${!reveal ? translateMarkup('<div class="timer" role="timer" aria-label="Time remaining"><span id="countdown">30</span><small>sec</small></div>') : html`<span class="tag">${room.revealComplete ? t('Round complete') : t('Host-led reveal')}</span>`}</div><span class="eyebrow">QUESTION ${room.questionNumber} OF 10</span><h2 class="question-prompt">${esc(room.prompt)}</h2>${content}</section>${scoreboard()}</div>`;
}
function finished() {
  const max = room.players[0]?.score, winners = room.players.filter(p => p.score===max);
  return html`<section class="room-card center final-card"><div class="lobby-icon" aria-hidden="true">✳</div><span class="eyebrow">TEN QUESTIONS. PLENTY OF LIES.</span><h2>${winners.length===1 ? t('The glory goes to…') : t('A shared taste of glory.')}</h2><p class="winner-name">${winners.map(p => esc(p.name)).join(' & ')}</p><p class="muted intro">${max} points. An impressive amount of questionable confidence.</p><ul class="player-list">${room.players.map((p,i) => person(p,i,true)).join('')}</ul><div class="button-row final-actions">${room.isHost ? html`<button class="btn" id="play-again">Play this quiz again ↗</button>` : ''}<a href="#home" class="btn secondary">Back home</a></div><p class="waiting-note">${room.isHost ? t('A replay creates a fresh room and resets everyone’s scores.') : t('Ask your host for a new room code to play again.')}</p></section>`;
}
function renderRoom() {
  preserveRender(html`<div class="room-wrap">${roomTop()}${room.phase==='lobby' ? lobby() : room.phase==='finished' ? finished() : live()}</div>`);
  $$('[data-action]').forEach(el => el.addEventListener('click',e => run(e, () => action(el.dataset.action))));
  $$('[data-reveal]').forEach(el => el.addEventListener('click',e => run(e, () => action('reveal',{playerId:el.dataset.reveal}))));
  $$('[data-copy]').forEach(el => el.addEventListener('click', () => copy(el.dataset.copy)));
  $$('[data-vote]').forEach(el => el.addEventListener('click',e => run(e, () => action('vote',{optionId:el.dataset.vote}))));
  $('#answer-form')?.addEventListener('submit', e => run(e, () => action('answer',{answer:$('#player-answer').value})));
  $('#play-again')?.addEventListener('click',e => run(e, () => createRoom(room.quizId)));
  updateTimer();
}
async function action(name, body = {}) {
  const currentRoute = route;
  const result = await api(`/sessions/${room.code}/${name}`,'POST',body,roomToken);
  if (route!==currentRoute) return;
  setRoom(result);
}
function setRoom(result) {
  clockOffset = new Date(result.serverTime).getTime()-Date.now();
  const previous = room;
  room = result;
  if (previous?.code === result.code && previous.questionNumber === result.questionNumber && (result.results?.length || 0) > (previous.results?.length || 0)) {
    for (const reveal of result.results.slice(previous.results?.length || 0)) {
      if (result.me && reveal.authorId === result.me) toast(t('Your bluff fooled {name}. +1 for you!', {name:nameOf(reveal.playerId)}));
    }
  }
  const signature = JSON.stringify({...result,serverTime:undefined});
  if (signature!==lastRoomSignature) { lastRoomSignature = signature; renderRoom(); }
}
async function copy(what) {
  const value = what==='code' ? room.code : html`${location.origin}${location.pathname}#join/${room.code}`;
  try { await navigator.clipboard.writeText(value); toast(what==='code' ? t('Room code copied.') : t('Invite link copied. Send it to your friends.')); }
  catch { toast(t('Share {value}', {value:what==='code' ? room.code : value})); }
}
function updateTimer() {
  if (!room || !$('#countdown')) return;
  const left = Math.max(0,Math.ceil((new Date(room.deadline).getTime()-Date.now()-clockOffset)/1000));
  $('#countdown').textContent = left;
  $('.timer')?.classList.toggle('urgent',left<=5);
  if (left===0) $$('[data-vote], #answer-form button').forEach(el => el.disabled=true);
}
async function pollRoom(initial = false) {
  if (loadingRoom) return;
  loadingRoom = true;
  const expectedRoute = route;
  try {
    const result = await api(`/sessions/${route.split('/')[1]}`,'GET',undefined,roomToken);
    if (route!==expectedRoute) return;
    pollFailures = 0; setRoom(result);
    if ($('#connection-state')) $('#connection-state').hidden=true;
  } catch (error) {
    if (route!==expectedRoute) return;
    pollFailures++;
    if (initial) {
      app.innerHTML = html`<section class="join-page room-card"><h2>Let’s get you in.</h2><p class="muted">${esc(t(error.message))}</p><div class="button-row final-actions"><a href="#join/${esc(route.split('/')[1])}" class="btn">Join as a player ↗</a><a href="#home" class="btn secondary">Home</a></div></section>`;
    } else if ($('#connection-state')) $('#connection-state').hidden=false;
  } finally { loadingRoom=false; }
}
async function navigate() {
  clearInterval(polling); polling=null; room=null; lastRoomSignature=''; pollFailures=0;
  route = location.hash.slice(1) || 'home';
  const [page,id,role] = route.split('/');
  $$('[data-nav]').forEach(el => el.toggleAttribute('aria-current',page===el.dataset.nav));
  document.title = page==='room' ? t('Game room · Options') : t('Options — The believable bluffing game');
  if (page==='home') home();
  else if (page==='library') library();
  else if (page==='editor') {
    const saved = quizzes.find(q => q.id===id);
    if (id!=='new' && !saved) { toast(t('Quiz not found in this browser.')); goto('library');return; }
    draft = store.get(`draft:${id}`) || structuredClone(saved || newDraft()); questionIndex=0; editor();
  } else if (page==='join') {
    app.innerHTML = html`<section class="join-page room-card"><span class="eyebrow">YOUR SEAT IS WAITING</span><h2>Bring your poker face.</h2><p class="muted">Enter a name and join the crew.</p>${joinForm(id || '')}</section>`;bindJoin();
  } else if (page==='room' && /^[A-Z2-9]{5}$/.test(id) && ['host','play'].includes(role)) {
    roomRole=role;roomToken=role==='play' ? store.get(`player:${id}`,'') : '';
    if (role==='play'&&!roomToken) { goto(`join/${id}`);return; }
    store.set('lastRoom',{code:id,role});app.innerHTML=translateMarkup('<p class="loading">Finding your crew…</p>');
    await pollRoom(true);
    if (route===`room/${id}/${role}`) polling=setInterval(() => pollRoom(),1000);
  } else goto('home');
  window.scrollTo({top:0,behavior:'instant'});
}
$('#rules-button').addEventListener('click', () => $('#rules').showModal());
$('#rules').addEventListener('click', e => { if (e.target.closest('.dialog-close')) $('#rules').close(); });
$('#rules').addEventListener('click',e => { if (e.target===$('#rules')) { const rect=e.target.getBoundingClientRect();if (e.clientX<rect.left||e.clientX>rect.right||e.clientY<rect.top||e.clientY>rect.bottom) e.target.close(); } });
window.addEventListener('hashchange',navigate);
document.addEventListener('visibilitychange', () => { if (!document.hidden && route.startsWith('room/')) pollRoom(); });
setInterval(updateTimer,200);
applyLanguage();
(async () => {
  app.innerHTML=translateMarkup('<p class="loading">Getting the game night ready…</p>');
  try { const data=await api('/bootstrap');quizzes=data.quizzes;await navigate(); }
  catch (error) { app.innerHTML=translateMarkup('<section class="empty-state"><h2>The game is taking a breath.</h2><p class="muted">We couldn’t connect to the server.</p><button class="btn" id="reload">Try again</button></section>');$('#reload').addEventListener('click',()=>location.reload());toast(error.message); }
})();
