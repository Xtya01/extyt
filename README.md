# extyt - Fixed for Koyeb

Ye wala version Koyeb par build fail nahi hoga.

### Koyeb par kaise fix kare (tere screenshot wala error)
1. Koyeb Dashboard > gay-harrietta/extyt > Settings pe ja
2. Builder ko "Dockerfile" select kar (Buildpack nahi)
3. Dockerfile location: /Dockerfile
4. Port: 8000

5. Ab naya zip wala code GitHub pe push kar:
   git add .
   git commit -m "fix koyeb build"
   git push

6. Fir Koyeb pe Redeploy dabaa

Logs dekhne ke liye: View latest deployment > Build logs

### Test
https://gay-harrietta-zill-e7f795ba.koyeb.app/
https://gay-harrietta-zill-e7f795ba.koyeb.app/convert?url=YOUR_OWN_YT_URL
