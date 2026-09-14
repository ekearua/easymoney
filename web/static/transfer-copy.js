// Copy-to-clipboard for the one-time bank transfer account number.
// Clipboard API needs a secure context; the execCommand fallback covers
// plain-http local runs.
(function(){
  var btn=document.querySelector('.xg-copy');
  if(!btn)return;
  btn.addEventListener('click',function(){
    var text=btn.getAttribute('data-copy');
    function done(){btn.textContent='✓ Copied';setTimeout(function(){btn.textContent='⧉ Copy account number';},1800);}
    if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(text).then(done,function(){fallback();});}else{fallback();}
    function fallback(){var ta=document.createElement('textarea');ta.value=text;ta.style.position='fixed';ta.style.opacity='0';document.body.appendChild(ta);ta.select();try{document.execCommand('copy');done();}catch(e){}document.body.removeChild(ta);}
  });
})();
