#include <getopt.h>
#include <stdio.h>
#include <stdlib.h>
int main(int argc, char **argv) {
 int flag = 0;
 struct option options[] = {
  {"alpha",0,0,'a'}, {"alpine",0,0,300}, {"beta",1,0,'b'},
  {"color",2,0,'c'}, {"verbose",0,&flag,1}, {"version",0,0,301},
  {"synonym",0,0,302}, {"synonyms",0,0,302}, {"f",0,0,303},
  {"foo",0,0,304}, {"optional",2,0,'o'}, {"required",1,0,'r'}, {0,0,0,0}
 };
 if (argv[3][0]=='1') setenv("POSIXLY_CORRECT","1",1);
 else unsetenv("POSIXLY_CORRECT");
 char *shorts = argv[1];
 int long_only = argv[2][0]=='1';
 argc -= 4; argv += 4;
 optind=0; opterr=0;
 for (;;) {
  int index=-1;
  int code = long_only ? getopt_long_only(argc,argv,shorts,options,&index)
                       : getopt_long(argc,argv,shorts,options,&index);
  if (code == -1) break;
  int error_option=(code=='?' || code==':') ? optopt : 0;
  printf("%d|%s|%s|%d|%d|%d\n",code,optarg?"true":"false",optarg?optarg:"",error_option,index,flag);
 }
 printf("end|%d|%d\n",optind,flag);
 for (int i=optind;i<argc;i++) printf("rest|%s\n",argv[i]);
 return 0;
}
